//go:build linux

package provider

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// This is a narrow stopped-engine epilogue, not a second recursive layer remover.
// Shutdown reacquires graph/layer locks; only bounded locks, empty indexes and
// empty VFS scaffolding are accepted. Unknown engine versions fail closed.
func buildMetadataKind(path string) string {
	switch path {
	case "storage.lock", "userns.lock", "vfs-layers/layers.lock":
		return "lock"
	case "vfs-layers/layers.json", "vfs-layers/volatile-layers.json":
		return "index"
	case "vfs", "vfs/dir", "vfs-layers":
		return "directory"
	default:
		return ""
	}
}

type buildMetadataNode struct {
	file     *os.File
	parent   int
	name     string
	path     string
	stat     unix.Stat_t
	children []*buildMetadataNode
}

func openBuildMetadata(parent int, name string) (*os.File, error) {
	// Reject devices/FIFOs before opening (and recheck after opening). O_NONBLOCK
	// bounds FIFO substitution; no legitimate metadata is a special file.
	var before unix.Stat_t
	if err := unix.Fstatat(parent, name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	kind := before.Mode & unix.S_IFMT
	if kind != unix.S_IFDIR && kind != unix.S_IFREG {
		return nil, errors.New("unsafe metadata type")
	}
	fd, err := unix.Openat2(parent, name, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK | unix.O_NOFOLLOW,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV,
	})
	if err != nil {
		return nil, err
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil || !sameBuildMetadata(before, after) {
		_ = unix.Close(fd)
		return nil, errors.New("metadata changed during open")
	}
	return os.NewFile(uintptr(fd), name), nil
}

func sameBuildMetadata(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode && a.Uid == b.Uid && a.Gid == b.Gid && a.Nlink == b.Nlink && a.Size == b.Size
}

func safeBuildMetadataOwner(stat unix.Stat_t) bool {
	return int(stat.Uid) == os.Geteuid() && int(stat.Gid) == os.Getegid() && stat.Mode&007022 == 0
}

func inspectBuildMetadata(ctx context.Context, parent int, name, path string, budget *int) (*buildMetadataNode, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	(*budget)--
	kind := buildMetadataKind(path)
	if *budget < 0 || kind == "" {
		return nil, errors.New("unrecognized or excessive metadata")
	}
	file, err := openBuildMetadata(parent, name)
	if err != nil {
		return nil, err
	}
	n := &buildMetadataNode{file: file, parent: parent, name: name, path: path}
	success := false
	defer func() {
		if !success {
			n.close()
		}
	}()
	if err := unix.Fstat(int(file.Fd()), &n.stat); err != nil {
		return nil, err
	}
	if !safeBuildMetadataOwner(n.stat) {
		return nil, errors.New("unsafe metadata owner or mode")
	}
	if kind == "directory" {
		if n.stat.Mode&unix.S_IFMT != unix.S_IFDIR || n.stat.Mode&0777 != 0700 {
			return nil, errors.New("metadata directory type or access mismatch")
		}
		entries, err := file.ReadDir(33)
		if (err != nil && err != io.EOF) || len(entries) > 32 {
			return nil, errors.New("metadata inventory exceeds bound")
		}
		for _, entry := range entries {
			child, err := inspectBuildMetadata(ctx, int(file.Fd()), entry.Name(), path+"/"+entry.Name(), budget)
			if err != nil {
				return nil, err
			}
			n.children = append(n.children, child)
		}
	} else {
		if n.stat.Mode&unix.S_IFMT != unix.S_IFREG || n.stat.Mode&0111 != 0 || n.stat.Nlink != 1 || n.stat.Size > 4096 {
			return nil, errors.New("unsafe metadata file")
		}
		if kind == "index" {
			raw, err := io.ReadAll(io.LimitReader(file, 4097))
			raw = bytes.TrimSpace(raw)
			if err != nil || len(raw) > 4096 || (!bytes.Equal(raw, []byte("[]")) && !bytes.Equal(raw, []byte("null"))) {
				return nil, errors.New("nonempty metadata index")
			}
		}
	}
	success = true
	return n, nil
}

func (n *buildMetadataNode) close() {
	for _, child := range n.children {
		child.close()
	}
	_ = n.file.Close()
}

func (n *buildMetadataNode) remove(ctx context.Context, verify func() error) error {
	for _, child := range n.children {
		if err := child.remove(ctx, verify); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := verify(); err != nil {
		return err
	}
	current, err := openBuildMetadata(n.parent, n.name)
	if err != nil {
		return err
	}
	defer current.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(int(current.Fd()), &stat); err != nil {
		return err
	}
	// Directory sizes/link counts change as our checked children are removed.
	if n.stat.Mode&unix.S_IFMT == unix.S_IFDIR {
		stat.Size, stat.Nlink = n.stat.Size, n.stat.Nlink
	}
	if !sameBuildMetadata(n.stat, stat) {
		return errors.New("metadata identity changed")
	}
	if buildMetadataKind(n.path) == "index" {
		raw, err := io.ReadAll(io.LimitReader(current, 4097))
		raw = bytes.TrimSpace(raw)
		if err != nil || len(raw) > 4096 || (!bytes.Equal(raw, []byte("[]")) && !bytes.Equal(raw, []byte("null"))) {
			return errors.New("metadata index changed")
		}
	}
	flags := 0
	if n.stat.Mode&unix.S_IFMT == unix.S_IFDIR {
		flags = unix.AT_REMOVEDIR
	}
	return unix.Unlinkat(n.parent, n.name, flags)
}

// Caller must have independently proved the cleanup unit stopped, including on
// cancellation. Pin the receipted scratch and original graph-root inode; openat2
// rejects symlinks and *all* mount crossings, including same-device bind mounts.
// No chmod/chown or mapped-owner deletion occurs in this host-side stage.
func (p *LocalDockerProvider) removeStoppedBuildMetadata(ctx context.Context, id, root string, owner buildOwnership, original os.FileInfo) error {
	fail := func() error {
		return errors.New("stopped build storage metadata unsafe or removal unconfirmed; ownership retained")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	verify := func() error {
		current, err := p.verifyActiveBuildOwnership(id)
		if err != nil || current != owner {
			return fail()
		}
		info, err := os.Lstat(filepath.Join(root, "storage"))
		if err != nil || !privateBuildDirectory(info) || !os.SameFile(original, info) {
			return fail()
		}
		return nil
	}
	// Legacy interrupted cleanup can have already removed storage. Still validate
	// both receipts before allowing runroot/scratch cleanup to proceed.
	if _, err := os.Lstat(filepath.Join(root, "storage")); errors.Is(err, os.ErrNotExist) {
		current, err := p.verifyActiveBuildOwnership(id)
		if err != nil || current != owner {
			return fail()
		}
		return nil
	}
	if err := verify(); err != nil {
		return err
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fail()
	}
	defer unix.Close(fd)
	var rootStat unix.Stat_t
	if unix.Fstat(fd, &rootStat) != nil || uint64(rootStat.Dev) != owner.RootDevice || rootStat.Ino != owner.RootInode {
		return fail()
	}
	store, err := openBuildMetadata(fd, "storage")
	if err != nil {
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) {
			return errors.New("stopped build storage cleanup requires kernel openat2 mount-safe resolution; ownership retained")
		}
		return fail()
	}
	defer store.Close()
	info, err := store.Stat()
	if err != nil || !privateBuildDirectory(info) || !os.SameFile(original, info) {
		return fail()
	}
	entries, err := store.ReadDir(33)
	if (err != nil && err != io.EOF) || len(entries) > 32 {
		return fail()
	}
	budget := 32
	nodes := []*buildMetadataNode{}
	defer func() {
		for _, node := range nodes {
			node.close()
		}
	}()
	// Validate the entire bounded tree before deleting any of its metadata.
	for _, entry := range entries {
		n, err := inspectBuildMetadata(ctx, int(store.Fd()), entry.Name(), entry.Name(), &budget)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fail()
		}
		nodes = append(nodes, n)
	}
	for _, n := range nodes {
		if err := n.remove(ctx, verify); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fail()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := verify(); err != nil {
		return err
	}
	// rmdir, never RemoveAll: unexpected children or mounts remain an error.
	if err := unix.Unlinkat(fd, "storage", unix.AT_REMOVEDIR); err != nil {
		return fail()
	}
	if _, err := os.Lstat(filepath.Join(root, "storage")); !errors.Is(err, os.ErrNotExist) {
		return fail()
	}
	return nil
}
