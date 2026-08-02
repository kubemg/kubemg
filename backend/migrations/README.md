# Schema migrations

**Nothing in this directory is executed.** KubeMG applies its schema through GORM
`AutoMigrate` in `pkg/db/db.go` (`db.Migrate`), which runs at boot before the
server accepts a request. That is the mechanism; these files are the record.

They exist because the schema is a deployment artefact for somebody who is not
running the binary. On an on-prem install the database is frequently owned by a
DBA who will not read Go struct tags, needs to review what an upgrade does to a
table they are responsible for, and may want to pre-apply it under change control
before the new image starts. A numbered `.sql` file is the form that conversation
takes.

The rules that keep them honest:

- Every statement is **idempotent** (`IF NOT EXISTS`, `IF EXISTS`), because
  `AutoMigrate` may already have applied it. Running a file after the server has
  booted must be a no-op rather than an error.
- A file is written **from** what `db.Migrate` does, not the other way round. If
  the two disagree, the Go code is what ran and the file is the bug.
- Column types match what the GORM driver emits for the model, so a
  pre-applied schema is one `AutoMigrate` then leaves alone.
