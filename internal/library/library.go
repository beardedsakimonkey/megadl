// Package library reconciles the on-disk media library with its persisted
// download records.
package library

import (
	"errors"
	"os"
	"path/filepath"

	"megadl/internal/db"
)

// Sync rebases persisted paths beneath root, then removes completed download
// records whose destination no longer exists. Rebasing first is important
// when the media directory (including its database) has been moved.
//
// Incomplete downloads are retained because their final destination may not
// exist yet and they may still have resumable partials.
func Sync(root string, database *db.DB) (int, error) {
	downloads, err := database.Downloads()
	if err != nil {
		return 0, err
	}

	pruned := 0
	for _, download := range downloads {
		destPath := filepath.Join(root, download.Name)
		if filepath.Clean(download.DestPath) != filepath.Clean(destPath) {
			if err := database.RebaseDownloadPath(download.ID, download.DestPath, destPath); err != nil {
				return pruned, err
			}
			download.DestPath = destPath
		}
		if download.Status != db.StatusDone {
			continue
		}
		if _, err := os.Lstat(download.DestPath); err == nil || !errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := database.DeleteDownload(download.ID); err != nil {
			return pruned, err
		}
		pruned++
	}
	return pruned, nil
}
