package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

var verbose bool

var textExtensions = map[string]bool{
	".yml":        true,
	".yaml":       true,
	".properties": true,
	".toml":       true,
	".conf":       true,
	".json":       true,
	".txt":        true,
	".cfg":        true,
	".ini":        true,
	".secret":     true,
}

func isTextConfig(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return textExtensions[ext]
}

type MCNode struct {
	fs.Inode

	realPath   string
	secrets    map[string]string
	reverseMap map[string]string
}

var _ = (fs.NodeLookuper)((*MCNode)(nil))
var _ = (fs.NodeReaddirer)((*MCNode)(nil))
var _ = (fs.NodeOpener)((*MCNode)(nil))
var _ = (fs.NodeGetattrer)((*MCNode)(nil))
var _ = (fs.NodeSetattrer)((*MCNode)(nil))
var _ = (fs.NodeCreater)((*MCNode)(nil))
var _ = (fs.NodeMkdirer)((*MCNode)(nil))
var _ = (fs.NodeUnlinker)((*MCNode)(nil))
var _ = (fs.NodeRmdirer)((*MCNode)(nil))
var _ = (fs.NodeRenamer)((*MCNode)(nil))
var _ = (fs.NodeSymlinker)((*MCNode)(nil))
var _ = (fs.NodeReadlinker)((*MCNode)(nil))
var _ = (fs.NodeLinker)((*MCNode)(nil))
var _ = (fs.NodeAccesser)((*MCNode)(nil))
var _ = (fs.NodeStatfser)((*MCNode)(nil))

func newMCNode(realPath string, secrets, reverseMap map[string]string) *MCNode {
	return &MCNode{
		realPath:   realPath,
		secrets:    secrets,
		reverseMap: reverseMap,
	}
}

func (n *MCNode) childNode(name string) *MCNode {
	return newMCNode(filepath.Join(n.realPath, name), n.secrets, n.reverseMap)
}

func (n *MCNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	childPath := filepath.Join(n.realPath, name)
	var st syscall.Stat_t
	if err := syscall.Lstat(childPath, &st); err != nil {
		if verbose {
			log.Printf("[LOOKUP] ENOENT: %s", childPath)
		}
		return nil, fs.ToErrno(err)
	}
	out.Attr.FromStat(&st)

	child := n.childNode(name)
	// Use real inode for stable identity (required by SQLite).
	stable := fs.StableAttr{Mode: st.Mode & syscall.S_IFMT, Ino: st.Ino}
	inode := n.NewInode(ctx, child, stable)
	// If go-fuse reused an existing node, update its realPath.
	if existing, ok := inode.Operations().(*MCNode); ok && existing.realPath != childPath {
		existing.realPath = childPath
	}
	return inode, fs.OK
}

func (n *MCNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries, err := os.ReadDir(n.realPath)
	if err != nil {
		return nil, fs.ToErrno(err)
	}

	result := make([]fuse.DirEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		st := info.Sys().(*syscall.Stat_t)
		result = append(result, fuse.DirEntry{
			Name: e.Name(),
			Mode: st.Mode & syscall.S_IFMT,
			Ino:  st.Ino,
		})
	}
	return fs.NewListDirStream(result), fs.OK
}

func (n *MCNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	if f != nil {
		if fga, ok := f.(fs.FileGetattrer); ok {
			return fga.Getattr(ctx, out)
		}
	}
	var st syscall.Stat_t
	if err := syscall.Lstat(n.realPath, &st); err != nil {
		return fs.ToErrno(err)
	}
	out.Attr.FromStat(&st)

	if isTextConfig(n.realPath) && (st.Mode&syscall.S_IFMT) == syscall.S_IFREG {
		data, err := os.ReadFile(n.realPath)
		if err == nil && strings.Contains(string(data), "${") {
			substituted, _ := substituteSecrets(data, n.secrets)
			out.Attr.Size = uint64(len(substituted))
		}
	}
	return fs.OK
}

func (n *MCNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if m, ok := in.GetMode(); ok {
		if err := os.Chmod(n.realPath, os.FileMode(m)); err != nil {
			return fs.ToErrno(err)
		}
	}
	if uid, ok := in.GetUID(); ok {
		gid := uint32(0xFFFFFFFF)
		if g, ok2 := in.GetGID(); ok2 {
			gid = g
		}
		if err := os.Lchown(n.realPath, int(uid), int(gid)); err != nil {
			return fs.ToErrno(err)
		}
	}
	if sz, ok := in.GetSize(); ok {
		// Avoid empty reads before flush.
		if !isTextConfig(n.realPath) {
			if err := os.Truncate(n.realPath, int64(sz)); err != nil {
				return fs.ToErrno(err)
			}
		}
	}
	var st syscall.Stat_t
	if err := syscall.Lstat(n.realPath, &st); err != nil {
		return fs.ToErrno(err)
	}
	out.Attr.FromStat(&st)
	return fs.OK
}

func (n *MCNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	isConfig := isTextConfig(n.realPath)
	if isConfig {
		fh := &mcFileHandle{
			realPath:   n.realPath,
			secrets:    n.secrets,
			reverseMap: n.reverseMap,
		}
		// Preserve truncation until flush.
		if flags&syscall.O_TRUNC != 0 {
			fh.writeBuf = []byte{}
			fh.hasWrite = true
		}
		return fh, fuse.FOPEN_DIRECT_IO, fs.OK
	}

	osFlags := int(flags) & (syscall.O_RDONLY | syscall.O_WRONLY | syscall.O_RDWR |
		syscall.O_APPEND | syscall.O_CREAT | syscall.O_EXCL | syscall.O_TRUNC | syscall.O_NOFOLLOW)
	f, err := os.OpenFile(n.realPath, osFlags, 0644)
	if err != nil {
		return nil, 0, fs.ToErrno(err)
	}
	return &passthroughFile{file: f, append: osFlags&syscall.O_APPEND != 0}, 0, fs.OK
}

func (n *MCNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	childPath := filepath.Join(n.realPath, name)
	f, err := os.OpenFile(childPath, int(flags)|os.O_CREATE, os.FileMode(mode))
	if err != nil {
		return nil, nil, 0, fs.ToErrno(err)
	}
	if verbose {
		log.Printf("[CREATE] %s (flags=0x%x mode=0%o)", childPath, flags, mode)
	}

	var st syscall.Stat_t
	if err := syscall.Lstat(childPath, &st); err != nil {
		f.Close()
		return nil, nil, 0, fs.ToErrno(err)
	}
	out.Attr.FromStat(&st)

	child := n.childNode(name)
	stable := fs.StableAttr{Mode: st.Mode & syscall.S_IFMT, Ino: st.Ino}
	inode := n.NewInode(ctx, child, stable)

	if isTextConfig(childPath) {
		f.Close()
		fh := &mcFileHandle{
			realPath:   childPath,
			secrets:    n.secrets,
			reverseMap: n.reverseMap,
		}
		return inode, fh, fuse.FOPEN_DIRECT_IO, fs.OK
	}
	return inode, &passthroughFile{file: f, append: int(flags)&syscall.O_APPEND != 0}, 0, fs.OK
}

func (n *MCNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	childPath := filepath.Join(n.realPath, name)
	if err := os.Mkdir(childPath, os.FileMode(mode)); err != nil {
		return nil, fs.ToErrno(err)
	}
	var st syscall.Stat_t
	if err := syscall.Lstat(childPath, &st); err != nil {
		return nil, fs.ToErrno(err)
	}
	out.Attr.FromStat(&st)
	child := n.childNode(name)
	stable := fs.StableAttr{Mode: st.Mode & syscall.S_IFMT, Ino: st.Ino}
	inode := n.NewInode(ctx, child, stable)
	return inode, fs.OK
}

func (n *MCNode) Unlink(ctx context.Context, name string) syscall.Errno {
	return fs.ToErrno(os.Remove(filepath.Join(n.realPath, name)))
}

func (n *MCNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	return fs.ToErrno(os.Remove(filepath.Join(n.realPath, name)))
}

func (n *MCNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	p := newParent.(*MCNode)
	oldPath := filepath.Join(n.realPath, name)
	newPath := filepath.Join(p.realPath, newName)
	if err := os.Rename(oldPath, newPath); err != nil {
		return fs.ToErrno(err)
	}
	// Update realPath on the moved node so reused inodes stay correct.
	if ch := n.GetChild(name); ch != nil {
		if moved, ok := ch.Operations().(*MCNode); ok {
			moved.realPath = newPath
		}
	}
	if verbose {
		log.Printf("[RENAME] %s → %s", oldPath, newPath)
	}
	return fs.OK
}

func (n *MCNode) Symlink(ctx context.Context, target, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	childPath := filepath.Join(n.realPath, name)
	if err := os.Symlink(target, childPath); err != nil {
		return nil, fs.ToErrno(err)
	}
	var st syscall.Stat_t
	if err := syscall.Lstat(childPath, &st); err != nil {
		return nil, fs.ToErrno(err)
	}
	out.Attr.FromStat(&st)
	child := n.childNode(name)
	stable := fs.StableAttr{Mode: st.Mode & syscall.S_IFMT, Ino: st.Ino}
	inode := n.NewInode(ctx, child, stable)
	return inode, fs.OK
}

func (n *MCNode) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	target, err := os.Readlink(n.realPath)
	if err != nil {
		return nil, fs.ToErrno(err)
	}
	return []byte(target), fs.OK
}

func (n *MCNode) Link(ctx context.Context, target fs.InodeEmbedder, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	t := target.(*MCNode)
	childPath := filepath.Join(n.realPath, name)
	if err := os.Link(t.realPath, childPath); err != nil {
		return nil, fs.ToErrno(err)
	}
	var st syscall.Stat_t
	if err := syscall.Lstat(childPath, &st); err != nil {
		return nil, fs.ToErrno(err)
	}
	out.Attr.FromStat(&st)
	child := n.childNode(name)
	stable := fs.StableAttr{Mode: st.Mode & syscall.S_IFMT, Ino: st.Ino}
	inode := n.NewInode(ctx, child, stable)
	return inode, fs.OK
}

func (n *MCNode) Access(ctx context.Context, mask uint32) syscall.Errno {
	return fs.ToErrno(syscall.Access(n.realPath, mask))
}

func (n *MCNode) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	var st syscall.Statfs_t
	if err := syscall.Statfs(n.realPath, &st); err != nil {
		return fs.ToErrno(err)
	}
	out.Blocks = st.Blocks
	out.Bfree = st.Bfree
	out.Bavail = st.Bavail
	out.Files = st.Files
	out.Ffree = st.Ffree
	out.Bsize = uint32(st.Bsize)
	out.NameLen = uint32(st.Namelen)
	out.Frsize = uint32(st.Frsize)
	return fs.OK
}

type mcFileHandle struct {
	realPath   string
	secrets    map[string]string
	reverseMap map[string]string
	mu         sync.Mutex
	writeBuf   []byte
	hasWrite   bool
}

var _ = (fs.FileReader)((*mcFileHandle)(nil))
var _ = (fs.FileWriter)((*mcFileHandle)(nil))
var _ = (fs.FileFlusher)((*mcFileHandle)(nil))
var _ = (fs.FileReleaser)((*mcFileHandle)(nil))
var _ = (fs.FileGetattrer)((*mcFileHandle)(nil))

func (fh *mcFileHandle) Read(ctx context.Context, buf []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	fh.mu.Lock()
	defer fh.mu.Unlock()

	data, err := os.ReadFile(fh.realPath)
	if err != nil {
		return nil, fs.ToErrno(err)
	}

	substituted, _ := substituteSecrets(data, fh.secrets)

	if off >= int64(len(substituted)) {
		return fuse.ReadResultData(nil), fs.OK
	}
	end := off + int64(len(buf))
	if end > int64(len(substituted)) {
		end = int64(len(substituted))
	}
	return fuse.ReadResultData(substituted[off:end]), fs.OK
}

func (fh *mcFileHandle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	fh.mu.Lock()
	defer fh.mu.Unlock()

	end := int(off) + len(data)
	if end > len(fh.writeBuf) {
		newBuf := make([]byte, end)
		copy(newBuf, fh.writeBuf)
		fh.writeBuf = newBuf
	}
	copy(fh.writeBuf[off:], data)
	fh.hasWrite = true
	// Return caller byte count.
	return uint32(len(data)), fs.OK
}

func (fh *mcFileHandle) Flush(ctx context.Context) syscall.Errno {
	fh.mu.Lock()
	defer fh.mu.Unlock()

	if !fh.hasWrite {
		return fs.OK
	}
	safe := reverseSubstitute(fh.writeBuf, fh.reverseMap)
	if err := os.WriteFile(fh.realPath, safe, 0644); err != nil {
		if verbose {
			log.Printf("[FLUSH] ERROR %s: %v", fh.realPath, err)
		}
		return fs.ToErrno(err)
	}
	if verbose {
		log.Printf("[FLUSH] %s (%d bytes written)", fh.realPath, len(safe))
	}
	fh.writeBuf = nil
	fh.hasWrite = false
	return fs.OK
}

func (fh *mcFileHandle) Release(ctx context.Context) syscall.Errno {
	return fh.Flush(ctx)
}

func (fh *mcFileHandle) Getattr(ctx context.Context, out *fuse.AttrOut) syscall.Errno {
	var st syscall.Stat_t
	if err := syscall.Stat(fh.realPath, &st); err != nil {
		return fs.ToErrno(err)
	}
	out.Attr.FromStat(&st)

	data, err := os.ReadFile(fh.realPath)
	if err == nil && strings.Contains(string(data), "${") {
		substituted, _ := substituteSecrets(data, fh.secrets)
		out.Attr.Size = uint64(len(substituted))
	}
	return fs.OK
}

type passthroughFile struct {
	file   *os.File
	append bool
}

var _ = (fs.FileReader)((*passthroughFile)(nil))
var _ = (fs.FileWriter)((*passthroughFile)(nil))
var _ = (fs.FileFlusher)((*passthroughFile)(nil))
var _ = (fs.FileReleaser)((*passthroughFile)(nil))
var _ = (fs.FileFsyncer)((*passthroughFile)(nil))
var _ = (fs.FileGetattrer)((*passthroughFile)(nil))
var _ = (fs.FileLseeker)((*passthroughFile)(nil))

func (pf *passthroughFile) Read(ctx context.Context, buf []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	n, err := pf.file.ReadAt(buf, off)
	if n == 0 && err != nil {
		return fuse.ReadResultData(nil), fs.ToErrno(err)
	}
	return fuse.ReadResultData(buf[:n]), fs.OK
}

func (pf *passthroughFile) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	var n int
	var err error
	if pf.append {
		n, err = pf.file.Write(data)
	} else {
		n, err = pf.file.WriteAt(data, off)
	}
	if err != nil {
		return uint32(n), fs.ToErrno(err)
	}
	return uint32(n), fs.OK
}

func (pf *passthroughFile) Flush(ctx context.Context) syscall.Errno {
	return fs.ToErrno(pf.file.Sync())
}

func (pf *passthroughFile) Release(ctx context.Context) syscall.Errno {
	return fs.ToErrno(pf.file.Close())
}

func (pf *passthroughFile) Fsync(ctx context.Context, flags uint32) syscall.Errno {
	return fs.ToErrno(pf.file.Sync())
}

func (pf *passthroughFile) Getattr(ctx context.Context, out *fuse.AttrOut) syscall.Errno {
	info, err := pf.file.Stat()
	if err != nil {
		return fs.ToErrno(err)
	}
	st := info.Sys().(*syscall.Stat_t)
	out.Attr.FromStat(st)
	return fs.OK
}

func (pf *passthroughFile) Lseek(ctx context.Context, off uint64, whence uint32) (uint64, syscall.Errno) {
	n, err := pf.file.Seek(int64(off), int(whence))
	if err != nil {
		return 0, fs.ToErrno(err)
	}
	return uint64(n), fs.OK
}
