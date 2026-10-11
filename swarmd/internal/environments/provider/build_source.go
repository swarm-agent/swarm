package provider

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"swarm-refactor/swarmtui/pkg/environments"
)

const maxBuildContext = int64(512 << 20)

// BuildSourceGitRouter routes the export's Git commands into the agent
// sandbox of an agent-writable repository (sandbox.RoutedArgv). The daemon
// installs it at startup; the provider cannot import the sandbox package,
// which builds on this one.
var BuildSourceGitRouter func(ctx context.Context, env []string, args ...string) (name string, argv []string, routed bool, err error)

func routeBuildSourceGit(ctx context.Context, env []string, args ...string) (string, []string, bool, error) {
	if BuildSourceGitRouter == nil {
		return "", nil, false, nil
	}
	return BuildSourceGitRouter(ctx, env, args...)
}

// ExportCommittedBuild creates no worktree and never copies live filesystem content.
// The caller supplies catalog-authorized roots. Git replacements, ambient config,
// attributes from the working tree and network access are disabled.
func ExportCommittedBuild(ctx context.Context, runner CommandRunner, productRoot, recipeRoot, destination string, b environments.ImageBuildDefinition) (string, error) {
	if err := b.Validate(); err != nil {
		return "", err
	}
	hash := sha256.New()
	total := int64(0)
	count := 0
	for _, source := range []struct{ root, commit, prefix, subtree string }{
		{productRoot, b.Product.Commit, "", ""},
		{recipeRoot, b.Recipe.Commit, ".swarm-recipe/", b.RecipeDirectory},
	} {
		gitEnv := []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1", "GIT_ATTR_NOSYSTEM=1"}
		gitArgs := []string{"--no-replace-objects", "--literal-pathspecs", "-C", source.root}
		// Repository configuration can define filters that git archive runs;
		// on an agent-writable repository those run inside its sandbox.
		gitCommand := func(extra ...string) (string, []string, error) {
			all := append(append([]string{}, gitArgs...), extra...)
			name, argv, routed, err := routeBuildSourceGit(ctx, gitEnv, all...)
			if err != nil {
				return "", nil, err
			}
			if routed {
				return name, argv, nil
			}
			return "env", append(append(append([]string{"-i"}, gitEnv...), "git"), all...), nil
		}
		name, revArgs, err := gitCommand("rev-parse", "--verify", source.commit+"^{commit}")
		if err != nil {
			return "", err
		}
		out, err := runner.Run(ctx, name, revArgs...)
		if err != nil || strings.TrimSpace(string(out)) != source.commit {
			return "", errors.New("exact committed build source is unavailable")
		}
		archive := []string{"-c", "core.attributesFile=/dev/null", "archive", "--format=tar", source.commit + "^{tree}"}
		if source.subtree != "" {
			archive = append(archive, "--", source.subtree)
		}
		archiveName, archiveArgs, err := gitCommand(archive...)
		if err != nil {
			return "", err
		}
		reader, writer := io.Pipe()
		done := make(chan error, 1)
		archiveCtx, cancel := context.WithCancel(ctx)
		go func() {
			err := runner.RunWithIO(archiveCtx, nil, writer, io.Discard, archiveName, archiveArgs...)
			_ = writer.CloseWithError(err)
			done <- err
		}()
		err = extractBuildArchive(ctx, reader, destination, source.prefix, hash, &total, &count)
		if err == nil {
			// tar EOF precedes archive padding and process exit. Drain bounded
			// trailing bytes before waiting, rather than cancelling a good export.
			n, drainErr := io.Copy(io.Discard, io.LimitReader(reader, 1<<20))
			if drainErr != nil || n == 1<<20 {
				err = errors.New("committed archive has invalid trailing data")
			}
		}
		_ = reader.Close()
		if err != nil {
			cancel()
		}
		runErr := <-done
		cancel()
		if err != nil {
			return "", err
		}
		if runErr != nil {
			return "", errors.New("committed build archive failed")
		}
	}
	recipe := filepath.Join(destination, ".swarm-recipe", filepath.FromSlash(b.RecipeFile))
	info, err := os.Lstat(recipe)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("committed recipe is absent or not a regular file")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func excludedBuildFile(name string) bool {
	for _, part := range strings.Split(strings.ToLower(name), "/") {
		if part == ".git" || part == ".ssh" || part == ".gnupg" || part == ".aws" || part == ".docker" || part == ".npmrc" || part == ".netrc" || strings.HasPrefix(part, ".env") || strings.HasSuffix(part, ".pem") || strings.HasSuffix(part, ".key") || part == "credentials" {
			return true
		}
	}
	return false
}

func extractBuildArchive(ctx context.Context, input io.Reader, dest, prefix string, digest io.Writer, total *int64, count *int) error {
	tr := tar.NewReader(io.LimitReader(input, maxBuildContext+1))
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return errors.New("invalid or oversized committed archive")
		}
		name := strings.TrimSuffix(h.Name, "/")
		if !environments.ValidBuildPath(name) {
			return errors.New("unsafe committed build path")
		}
		*count++
		*total += h.Size
		if *count > 30000 || h.Size < 0 || *total > maxBuildContext {
			return errors.New("build context exceeds file or byte limit")
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir {
			return errors.New("build context forbids symlinks, hardlinks and special files")
		}
		if excludedBuildFile(name) {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(prefix+name))
		if h.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, 0700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		mode := os.FileMode(0600)
		if h.Mode&0111 != 0 {
			mode = 0700
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(digest, "%s\x00%d\x00%d\x00", prefix+name, mode, h.Size)
		_, err = io.CopyN(io.MultiWriter(file, digest), tr, h.Size)
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
}
