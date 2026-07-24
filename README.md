# megadl

A terminal UI for downloading mega.nz links and managing the resulting local
media library. It implements the public MEGA file and folder protocols directly
in Go.

## Building

```sh
go build -o megadl .
./megadl
```

## Configuration

`~/.config/megadl/config.json` (created on first run):

```json
{
  "download_dir": "~/Media/mega"
}
```

Download history and quota accounting live in `<download_dir>/.megadl.db`
(SQLite). Deleting a history entry is permanent and cascades to its per-file
records; bytes already counted toward the quota view are kept.

## Tests

```sh
go test ./...
```

Engine tests use an in-memory driver, and protocol tests run against a local
fake MEGA server. No external downloader or network connection is needed.
