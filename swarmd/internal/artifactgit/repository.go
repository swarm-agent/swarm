package artifactgit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
var objectPattern = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

const manifestPath = "swarm-artifact.json"

// Open creates or opens one private bare repository below root. root must be the
// canonical daemon artifact repository directory; repository identity is not
// derived from a workspace path.
func Open(ctx context.Context, root, repositoryID string, limits Limits) (*Repository, error) {
	if !idPattern.MatchString(repositoryID) || repositoryID == "." || repositoryID == ".." { return nil, invalid("repository id") }
	git, err := exec.LookPath("git"); if err != nil { return nil, fmt.Errorf("artifactgit: native git required: %w", err) }
	if err := os.MkdirAll(root, 0o700); err != nil { return nil, err }
	if err := ensurePrivate(root, true); err != nil { return nil, err }
	hooks := filepath.Join(root, ".empty-hooks")
	if err := os.MkdirAll(hooks, 0o700); err != nil { return nil, err }
	path := filepath.Join(root, repositoryID+".git")
	r := &Repository{root: root, path: path, hooks: hooks, git: git, id: repositoryID, limits: limits.normalized()}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if _, err = r.raw(ctx, nil, "init", "--bare", "--initial-branch=official", path); err != nil { return nil, err }
	} else if err != nil { return nil, err }
	if err := ensurePrivate(path, true); err != nil { return nil, err }
	out, err := r.gitCmd(ctx, nil, "rev-parse", "--is-bare-repository"); if err != nil || strings.TrimSpace(string(out)) != "true" { return nil, fmt.Errorf("%w: not a bare repository", ErrIntegrity) }
	return r, nil
}

func ensurePrivate(path string, directory bool) error {
	info, err := os.Lstat(path); if err != nil { return err }
	if info.Mode()&os.ModeSymlink != 0 { return fmt.Errorf("%w: symlink storage", ErrIntegrity) }
	if directory && !info.IsDir() { return fmt.Errorf("%w: storage is not directory", ErrIntegrity) }
	if info.Mode().Perm()&0o077 != 0 { if err := os.Chmod(path, 0o700); err != nil { return err } }
	return nil
}

func (r *Repository) env() []string {
	return append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=Swarm Artifact", "GIT_AUTHOR_EMAIL=artifact@swarm.invalid", "GIT_COMMITTER_NAME=Swarm Artifact", "GIT_COMMITTER_EMAIL=artifact@swarm.invalid", "GIT_AUTHOR_DATE=2000-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z")
}

func (r *Repository) raw(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, r.git, args...); cmd.Env = r.env(); if input != nil { cmd.Stdin = bytes.NewReader(input) }
	out, err := cmd.CombinedOutput(); if err != nil { return nil, fmt.Errorf("artifactgit: git %s: %w: %s", args[0], err, strings.TrimSpace(string(out))) }; return out, nil
}
func (r *Repository) gitCmd(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	base := []string{"--git-dir="+r.path, "-c", "core.hooksPath="+r.hooks, "-c", "core.attributesFile="+os.DevNull, "-c", "commit.gpgSign=false", "-c", "tag.gpgSign=false", "-c", "protocol.file.allow=never", "-c", "protocol.allow=never"}
	return r.raw(ctx, input, append(base, args...)...)
}

func (r *Repository) Genesis(ctx context.Context, value Genesis) (string, error) {
	if head, err := r.ref(ctx, "refs/heads/official"); err == nil { return head, nil }
	m, err := r.manifestFromGenesis(ctx, value); if err != nil { return "", err }
	commit, err := r.commit(ctx, m, nil, "artifact genesis"); if err != nil { return "", err }
	if err := r.updateRef(ctx, "refs/heads/official", commit, strings.Repeat("0", len(commit))); err != nil { return "", err }
	return commit, nil
}

func (r *Repository) manifestFromGenesis(ctx context.Context, g Genesis) (Manifest, error) {
	m := Manifest{Version: ManifestVersion, MediaType: g.MediaType}
	if g.Content != nil && len(g.Parts) > 0 { return m, invalid("content and parts are mutually exclusive") }
	if g.Content != nil { p, err := r.storeBlob(ctx, "content", *g.Content); if err != nil { return m, err }; m.Content = &p }
	ids := make([]string, 0, len(g.Parts)); for id := range g.Parts { ids = append(ids, id) }; sort.Strings(ids)
	if len(ids) > r.limits.MaxParts { return m, ErrQuotaExceeded }
	for _, id := range ids { p, err := r.storeBlob(ctx, id, g.Parts[id]); if err != nil { return m, err }; m.Parts = append(m.Parts, p) }
	if m.Content == nil && len(m.Parts) == 0 { return m, invalid("empty artifact") }; return m, nil
}

func (r *Repository) storeBlob(ctx context.Context, id string, in BlobInput) (Part, error) {
	if !idPattern.MatchString(id) { return Part{}, invalid("part id") }
	if int64(len(in.Bytes)) > r.limits.MaxBlobBytes { return Part{}, ErrQuotaExceeded }
	out, err := r.gitCmd(ctx, in.Bytes, "hash-object", "-w", "--stdin"); if err != nil { return Part{}, err }
	return Part{ID:id, MediaType:in.MediaType, Blob:strings.TrimSpace(string(out)), Size:int64(len(in.Bytes))}, nil
}

func (r *Repository) commit(ctx context.Context, m Manifest, parents []string, message string) (string, error) {
	if err := validateManifest(m, r.limits); err != nil { return "", err }
	body, err := json.Marshal(m); if err != nil { return "", err }; body = append(body, '\n')
	manifestBlob, err := r.gitCmd(ctx, body, "hash-object", "-w", "--stdin"); if err != nil { return "", err }
	var tree bytes.Buffer
	fmt.Fprintf(&tree, "100644 blob %s\t%s\n", strings.TrimSpace(string(manifestBlob)), manifestPath)
	if m.Content != nil { fmt.Fprintf(&tree, "100644 blob %s\tcontent\n", m.Content.Blob) }
	if len(m.Parts) > 0 {
		var partsTree bytes.Buffer
		for _, p := range m.Parts { fmt.Fprintf(&partsTree, "100644 blob %s\t%s\n", p.Blob, p.ID) }
		partsTreeID, treeErr := r.gitCmd(ctx, partsTree.Bytes(), "mktree"); if treeErr != nil { return "", treeErr }
		fmt.Fprintf(&tree, "040000 tree %s\tparts\n", strings.TrimSpace(string(partsTreeID)))
	}
	treeID, err := r.gitCmd(ctx, tree.Bytes(), "mktree"); if err != nil { return "", err }
	args := []string{"commit-tree", strings.TrimSpace(string(treeID))}; for _, p := range parents { if !objectPattern.MatchString(p) { return "", invalid("parent") }; args = append(args, "-p", p) }; args = append(args, "-m", boundedMessage(message))
	out, err := r.gitCmd(ctx, nil, args...); if err != nil { return "", err }; return strings.TrimSpace(string(out)), nil
}

func boundedMessage(s string) string { s = strings.TrimSpace(s); if s == "" { return "Swarm artifact revision" }; if len(s)>1024 { return s[:1024] }; return s }

func validateManifest(m Manifest, limits Limits) error {
	if m.Version != ManifestVersion || (m.Content == nil) == (len(m.Parts)==0) { return fmt.Errorf("%w: invalid manifest shape", ErrIntegrity) }
	if len(m.Parts)>limits.MaxParts { return ErrQuotaExceeded }; var total int64; seen:=map[string]bool{}
	all:=m.Parts; if m.Content!=nil { all=[]Part{*m.Content} }
	for _,p:=range all { if !idPattern.MatchString(p.ID)||!objectPattern.MatchString(p.Blob)||p.Size<0||p.Size>limits.MaxBlobBytes||seen[p.ID] { return fmt.Errorf("%w: invalid part",ErrIntegrity) }; seen[p.ID]=true; total+=p.Size }
	if total>limits.MaxCompositionBytes{return ErrQuotaExceeded}; return nil
}

func (r *Repository) ReadCommit(ctx context.Context, id string) (Commit, error) {
	if !objectPattern.MatchString(id) { return Commit{}, invalid("commit") }
	typeOut, err := r.gitCmd(ctx,nil,"cat-file","-t",id); if err!=nil||strings.TrimSpace(string(typeOut))!="commit" { return Commit{},ErrNotFound }
	parentOut, err:=r.gitCmd(ctx,nil,"show","-s","--format=%P",id); if err!=nil{return Commit{},err}; parents:=strings.Fields(string(parentOut))
	data,err:=r.gitCmd(ctx,nil,"show",id+":"+manifestPath); if err!=nil{return Commit{},err}; var m Manifest; if json.Unmarshal(data,&m)!=nil{return Commit{},ErrIntegrity}; if err:=validateManifest(m,r.limits);err!=nil{return Commit{},err}
	return Commit{ID:id,Parents:parents,Manifest:m},nil
}

func (r *Repository) ReadBlob(ctx context.Context, commit, partID string) ([]byte,error){ c,err:=r.ReadCommit(ctx,commit);if err!=nil{return nil,err}; p,ok:=findPart(c.Manifest,partID);if !ok{return nil,ErrNotFound}; out,err:=r.gitCmd(ctx,nil,"cat-file","blob",p.Blob);if err!=nil{return nil,err};if int64(len(out))!=p.Size{return nil,ErrIntegrity};return out,nil }
func findPart(m Manifest,id string)(Part,bool){if m.Content!=nil&&(id=="content"||id==m.Content.ID){return *m.Content,true};for _,p:=range m.Parts{if p.ID==id{return p,true}};return Part{},false}

func (r *Repository) Candidate(ctx context.Context, req CandidateRequest)(string,error){
	if !idPattern.MatchString(req.ID){return "",invalid("candidate id")}; base,err:=r.ReadCommit(ctx,req.Base);if err!=nil{return "",err}; m:=base.Manifest
	if req.Content!=nil { if m.Content==nil{return "",invalid("content change on multipart artifact")}; if m.Content.Locked{return "",ErrLockedPart}; p,err:=r.storeBlob(ctx,"content",*req.Content);if err!=nil{return "",err};p.Locked=m.Content.Locked;m.Content=&p }
	for id,ch:=range req.Parts { if !idPattern.MatchString(id){return "",invalid("part id")}; idx:=-1;for i:=range m.Parts{if m.Parts[i].ID==id{idx=i;break}};if idx<0{return "",ErrNotFound};if m.Parts[idx].Locked&&len(ch.Bytes)>0{return "",ErrLockedPart};if len(ch.Bytes)>0{p,err:=r.storeBlob(ctx,id,BlobInput{MediaType:ch.MediaType,Bytes:ch.Bytes});if err!=nil{return "",err};p.Locked=m.Parts[idx].Locked;m.Parts[idx]=p};if ch.Lock!=nil{m.Parts[idx].Locked=*ch.Lock} }
	commit,err:=r.commit(ctx,m,[]string{req.Base},req.Message);if err!=nil{return "",err}; ref:="refs/swarm/candidates/"+req.ID;if old,e:=r.ref(ctx,ref);e==nil{if old==commit{return commit,nil};return "",ErrConflict};if err:=r.updateRef(ctx,ref,commit,strings.Repeat("0",len(commit)));err!=nil{return "",err};return commit,nil
}

func (r *Repository) Merge(ctx context.Context, req MergeRequest)(string,error){
	if !idPattern.MatchString(req.ID)||len(req.Parents)<2{return "",invalid("merge")}; commits:=make(map[string]Commit,len(req.Parents));for _,id:=range req.Parents{c,e:=r.ReadCommit(ctx,id);if e!=nil{return "",e};commits[id]=c}; base:=commits[req.Parents[0]].Manifest
	for partID,sel:=range req.Selections { c,ok:=commits[sel.Commit];if !ok{return "",invalid("selection parent")};sourceID:=sel.PartID;if sourceID==""{sourceID=partID};p,ok:=findPart(c.Manifest,sourceID);if !ok{return "",ErrNotFound};current,ok:=findPart(base,partID);if !ok{return "",ErrNotFound};if current.Locked&&current.Blob!=p.Blob{return "",ErrLockedPart};p.ID=partID;if sel.Lock!=nil{p.Locked=*sel.Lock};if base.Content!=nil{base.Content=&p}else{for i:=range base.Parts{if base.Parts[i].ID==partID{base.Parts[i]=p}}} }
	commit,err:=r.commit(ctx,base,req.Parents,req.Message);if err!=nil{return "",err};ref:="refs/swarm/candidates/"+req.ID;if old,e:=r.ref(ctx,ref);e==nil{if old==commit{return commit,nil};return "",ErrConflict};if err:=r.updateRef(ctx,ref,commit,strings.Repeat("0",len(commit)));err!=nil{return "",err};return commit,nil
}

func (r *Repository) AdvanceOfficial(ctx context.Context, expected, next, transactionID string)(string,error){
	if !idPattern.MatchString(transactionID)||!objectPattern.MatchString(expected)||!objectPattern.MatchString(next){return "",invalid("transaction")};if _,e:=r.ReadCommit(ctx,next);e!=nil{return "",e};tx:="refs/swarm/transactions/"+transactionID
	if old,e:=r.ref(ctx,tx);e==nil{if old==next{return old,nil};return "",ErrTransactionReuse}
	input:=fmt.Sprintf("start\nupdate refs/heads/official %s %s\ncreate %s %s\nprepare\ncommit\n",next,expected,tx,next);if _,err:=r.gitCmd(ctx,[]byte(input),"update-ref","--stdin");err!=nil{return "",fmt.Errorf("%w: %v",ErrConflict,err)};return next,nil
}

func (r *Repository) ref(ctx context.Context,name string)(string,error){out,err:=r.gitCmd(ctx,nil,"rev-parse","--verify",name+"^{commit}");if err!=nil{return "",ErrNotFound};return strings.TrimSpace(string(out)),nil}
func (r *Repository) updateRef(ctx context.Context,name,next,old string)error{_,err:=r.gitCmd(ctx,nil,"update-ref",name,next,old);return err}
func (r *Repository) Official(ctx context.Context)(string,error){return r.ref(ctx,"refs/heads/official")}
func (r *Repository) ListRefs(ctx context.Context,prefix string)([]Ref,error){
	allowed:=map[string]bool{"refs/swarm/candidates/":true,"refs/swarm/transactions/":true};if !allowed[prefix]{return nil,invalid("ref prefix")};out,err:=r.gitCmd(ctx,nil,"for-each-ref","--format=%(refname) %(objectname)",prefix);if err!=nil{return nil,err};lines:=strings.Split(strings.TrimSpace(string(out)),"\n");refs:=[]Ref{};for _,line:=range lines{if line==""{continue};f:=strings.Fields(line);if len(f)!=2{return nil,ErrIntegrity};refs=append(refs,Ref{Name:f[0],Commit:f[1]})};if len(refs)>r.limits.MaxRefs{return nil,ErrQuotaExceeded};return refs,nil
}

func (r *Repository) IntegrityCheck(ctx context.Context)error{_,err:=r.gitCmd(ctx,nil,"fsck","--full","--strict");return err}
func (r *Repository) Bundle(ctx context.Context,dst string)error{if !filepath.IsAbs(dst){return invalid("bundle path")};if err:=os.MkdirAll(filepath.Dir(dst),0o700);err!=nil{return err};_,err:=r.gitCmd(ctx,nil,"bundle","create",dst,"--all");if err==nil{err=os.Chmod(dst,0o600)};return err}
func (r *Repository) Delete()error{if filepath.Dir(r.path)!=filepath.Clean(r.root)||filepath.Base(r.path)!=r.id+".git"{return ErrIntegrity};return os.RemoveAll(r.path)}
func (r *Repository) Materialize(ctx context.Context,commit,dst string)error{
	c,err:=r.ReadCommit(ctx,commit);if err!=nil{return err};if err:=os.MkdirAll(dst,0o700);err!=nil{return err};if c.Manifest.Content!=nil{b,e:=r.ReadBlob(ctx,commit,"content");if e!=nil{return e};return os.WriteFile(filepath.Join(dst,"content"),b,0o600)};for _,p:=range c.Manifest.Parts{b,e:=r.ReadBlob(ctx,commit,p.ID);if e!=nil{return e};if e=os.WriteFile(filepath.Join(dst,p.ID),b,0o600);e!=nil{return e}};return nil
}

