package db

// CollapsedDirs returns folder paths relative to each download's destination.
func (d *DB) CollapsedDirs() (map[int64]map[string]bool, error) {
	rows, err := d.sql.Query(`SELECT download_id, path FROM collapsed_dirs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	folders := make(map[int64]map[string]bool)
	for rows.Next() {
		var id int64
		var path string
		if err := rows.Scan(&id, &path); err != nil {
			return nil, err
		}
		if folders[id] == nil {
			folders[id] = make(map[string]bool)
		}
		folders[id][path] = true
	}
	return folders, rows.Err()
}

// SetCollapsedDirs replaces fold state for the supplied downloads only.
func (d *DB) SetCollapsedDirs(folders map[int64][]string) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for id, paths := range folders {
		if _, err := tx.Exec(`DELETE FROM collapsed_dirs WHERE download_id = ?`, id); err != nil {
			return err
		}
		for _, path := range paths {
			// A download can be deleted while a UI save is in flight.
			if _, err := tx.Exec(`INSERT OR IGNORE INTO collapsed_dirs (download_id, path)
				SELECT id, ? FROM downloads WHERE id = ?`, path, id); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
