package vfs

import (
	"encoding/json"
	"os"
	"path"
)

// FsckLogType is the type of a FsckLog
type FsckLogType int

const (
	// FileMissing used when a file data is missing from its index entry.
	FileMissing FsckLogType = iota
	// IndexMissing is used when the index entry is missing from a file data.
	IndexMissing
	// TypeMismatch is used when a document type does not match in the index and
	// underlying filesystem.
	TypeMismatch
	// ContentMismatch is used when a document content checksum does not match
	// with the one in the underlying fs.
	ContentMismatch
)

// FsckLog is a struct for an inconsistency in the VFS
type FsckLog struct {
	Type        FsckLogType
	FileDoc     *FileDoc
	OldFileDoc  *FileDoc
	IsFile      bool
	DirDoc      *DirDoc
	Filename    string
	PruneAction string
	PruneError  error
}

// String returns a string describing the FsckLog
func (f *FsckLog) String() string {
	switch f.Type {
	case FileMissing:
		if f.IsFile {
			return "the file is present in the index but not on the filesystem"
		}
		return "the directory is present in the index but not on the filesystem"
	case TypeMismatch:
		if f.IsFile {
			return "it's a file in the index but a directory on the filesystem"
		}
		return "it's a directory in the index but a file on the filesystem"
	case IndexMissing:
		return "the document is present on the local filesystem but not in the index"
	case ContentMismatch:
		return "then document content does not match the store content checksum"
	}
	panic("bad FsckLog type")
}

// MarshalJSON implements the json.Marshaler interface
func (f *FsckLog) MarshalJSON() ([]byte, error) {
	v := map[string]string{
		"filename": f.Filename,
		"message":  f.String(),
	}
	if f.IsFile {
		v["file_id"] = f.FileDoc.ID()
	} else {
		v["file_id"] = f.DirDoc.ID()
	}
	if f.PruneAction != "" {
		v["prune_action"] = f.PruneAction
		if f.PruneError != nil {
			v["prune_error"] = f.PruneError.Error()
		}
	}
	return json.Marshal(v)
}

// FsckPrune tries to fix the given entry in the VFS
func FsckPrune(fs VFS, indexer Indexer, entry *FsckLog, dryrun bool) {
	switch entry.Type {
	case FileMissing:
		entry.PruneAction = "deleting entry from index"
		if dryrun {
			return
		}
		if entry.IsFile {
			if err := indexer.DeleteFileDoc(entry.FileDoc); err != nil {
				entry.PruneError = err
			}
		} else {
			if err := indexer.DeleteDirDoc(entry.DirDoc); err != nil {
				entry.PruneError = err
			}
		}
	case IndexMissing:
		if !entry.IsFile {
			return
		}
		fileDoc := entry.FileDoc
		var orphan bool
		if fileDoc.DirID == "" {
			orphan = true
		} else {
			parentDir, err := indexer.DirByID(fileDoc.DirID)
			if os.IsNotExist(err) {
				orphan = true
			} else if err != nil {
				entry.PruneError = err
				return
			} else {
				fullpath := path.Join(parentDir.Fullpath, fileDoc.Name())
				if _, err := indexer.FileByPath(fullpath); err != nil {
					entry.PruneError = err
					return
				}
			}
		}
		if orphan {
			entry.PruneAction = "creating index entry in orphan directory"
		} else {
			entry.PruneAction = "creating index entry in-place"
		}
		if dryrun {
			return
		}
		if orphan {
			orphanDir, err := Mkdir(fs, OrphansDirName, nil)
			if err != nil {
				entry.PruneError = err
				return
			}
			fileDoc.DirID = orphanDir.ID()
		}
		if err := indexer.CreateFileDoc(fileDoc); err != nil {
			entry.PruneError = err
		}
	case ContentMismatch:
		if !entry.IsFile {
			return
		}
		entry.PruneAction = "updating the index informations to match the stored data"
		if err := indexer.UpdateFileDoc(entry.OldFileDoc, entry.FileDoc); err != nil {
			entry.PruneError = err
		}
	}
}
