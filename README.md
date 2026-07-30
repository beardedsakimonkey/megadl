# megadl

A terminal UI for downloading mega.nz links and managing the resulting local
media library. It implements the public MEGA file and folder protocols directly
in Go (based on [megatools](https://xff.cz/megatools/) code).

<img width="1610" height="985" alt="screenshot" src="https://github.com/user-attachments/assets/940dd58e-d451-4206-acc9-75dedfb831c3" />

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
