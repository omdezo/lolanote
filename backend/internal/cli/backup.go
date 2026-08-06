package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// OPS1 — THERE WAS NO BACKUP.
//
// Not a broken one, not an untested one: nothing in the repository, the compose
// file, the Makefile, the CI workflow or four planning documents mentions
// mongodump, a volume snapshot, or a restore procedure. The whole of the
// product's durability was one named docker volume (`mongo-data`), which
// `docker compose down -v` removes without a prompt, and which no second machine
// has ever held a copy of. A person's boards, their agent history, their labels,
// their comments and their trash all live in that one directory on one host.
//
// This is the smallest honest thing: a documented, scriptable dump and a restore
// that refuses to run by accident. It is deliberately a wrapper around
// mongodump/mongorestore rather than a hand-written exporter, because the
// database's own tools already handle BSON fidelity, indexes and large
// collections correctly, and a bespoke exporter is a second thing to be wrong.
//
// WHAT THIS DOES NOT PROTECT — stated here rather than discovered during an
// incident:
//
//   - MongoDB runs as a STANDALONE node (compose starts `mongo:7` with no
//     --replSet). No oplog, therefore no --oplog dump, therefore NO
//     point-in-time recovery: the recovery point is the last backup and the
//     recovery-point objective equals the backup interval, nothing smaller.
//     Standalone also means no multi-document transactions, which is why the
//     write path compensates by hand (see TransactionService.compensate) — and
//     it means a dump taken while writes are landing is consistent WITHIN each
//     collection and not ACROSS them. A transaction whose element writes were
//     captured but whose journal row was not restores as changes with no
//     inverse. Backing up against a quiet period is the mitigation available
//     without a replica set.
//   - UPLOADED FILES ARE NOT IN THE DUMP. Under STORAGE_DRIVER=local they are in
//     the `api-uploads` volume; under r2 they are in the bucket. Restoring this
//     archive alone gives every image and file card a row pointing at bytes that
//     are gone.
//   - KEYCLOAK IS A DIFFERENT DATABASE. Users, credentials and sessions live in
//     the Postgres beside it. Restore Mongo without it and every board is intact
//     and nobody can log in to reach one: ACLs key on the Keycloak subject id.
//
// The command prints all three every time it succeeds, because an operator who
// has a backup they believe is complete is worse off than one who knows what is
// missing.

// backupOptions are the flags, held apart from the cobra command so the parts
// worth testing are functions rather than a RunE closure.
type backupOptions struct {
	dir  string
	keep int
	tool string
}

var backupOpts = backupOptions{dir: "./backups", keep: 14, tool: "mongodump"}

// ArchiveName is the file one backup run produces.
//
// UTC and sortable, so `ls` is chronological, retention is a prefix sort, and a
// name never depends on the operator's timezone. The database name is in it
// because one host can hold staging and production.
func ArchiveName(db string, at time.Time) string {
	return fmt.Sprintf("%s-%s.archive.gz", db, at.UTC().Format("20060102T150405Z"))
}

// BackupArgs is the exact mongodump invocation, as a function so the flags can
// be asserted without a database or a mongodump binary present.
//
// --archive + --gzip produces ONE file rather than a directory tree: a single
// file is what an operator can checksum, copy off the host and hand to
// mongorestore without wondering whether a subdirectory went missing.
//
// There is deliberately no --oplog. It requires a replica set, and this
// deployment is a standalone node; passing it would fail at the worst possible
// moment, and pretending the dump is point-in-time when it is not would be the
// more expensive lie.
func BackupArgs(uri, db, archive string) []string {
	return []string{
		"--uri=" + uri,
		"--db=" + db,
		"--archive=" + archive,
		"--gzip",
	}
}

// RestoreArgs is the matching mongorestore invocation.
//
// --drop is not optional and not a flag. A restore into a database that still
// holds rows merges the two, so a partial disaster becomes a database containing
// both the survivors and the backup's older copies of the same _ids — which is
// worse than either, and is the state an operator is least able to reason about
// at 3am. The command refuses without an explicit confirmation instead.
func RestoreArgs(uri, db, archive string) []string {
	return []string{
		"--uri=" + uri,
		"--nsInclude=" + db + ".*",
		"--drop",
		"--archive=" + archive,
		"--gzip",
	}
}

// PruneList picks the archives retention should delete: everything except the
// `keep` newest.
//
// Returned rather than deleted so the decision is testable and so a caller can
// print it. keep <= 0 deletes nothing — retention that can be misconfigured into
// "delete everything" is a backup system that occasionally is the disaster.
func PruneList(names []string, keep int) []string {
	if keep <= 0 || len(names) <= keep {
		return nil
	}
	sorted := append([]string(nil), names...)
	sort.Sort(sort.Reverse(sort.StringSlice(sorted))) // names sort chronologically
	return sorted[keep:]
}

// archivesIn lists this database's archives in one directory.
func archivesIn(dir, db string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), db+"-") && strings.HasSuffix(e.Name(), ".archive.gz") {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// ErrToolMissing names the one prerequisite, and names the fix.
//
// The API image is alpine with the Go binary and nothing else — mongodb-tools is
// not installed, deliberately, because shipping a database client in the API
// image widens what a compromised API container can reach. So this command is
// run from the host, or from a `mongo:7` container mounted against the same
// volume. Saying that here is the difference between a five-minute fix and an
// operator concluding the feature does not work.
var ErrToolMissing = errors.New(
	"mongodump/mongorestore not found on PATH — install mongodb-database-tools, " +
		"or run this from a mongo image: " +
		`docker run --rm --network qomranote_default -v "$PWD/backups:/backups" mongo:7 ` +
		`mongodump --uri=mongodb://mongo:27017 --db=qomranote --archive=/backups/qomranote.archive.gz --gzip`)

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Write a compressed archive of the MongoDB database",
	Long: `Dump the configured MongoDB database to a single gzipped archive.

Uses mongodump, so the archive restores with the database's own tooling and does
not depend on this binary's version.

WHAT IT COVERS: every collection in MONGO_DB — elements, transactions, comments,
labels, notifications, agent runs and the agent journal.

WHAT IT DOES NOT COVER, and no wrapper can:

  * Point-in-time recovery (PITR). MongoDB here is a STANDALONE node with no replica
    set, so there is no oplog to roll forward. The recovery point is the last
    archive; the RPO equals the backup interval.
  * Cross-collection consistency under load. Without transactions, a dump taken
    while writes land can capture an element write whose journal row it missed.
    Prefer a quiet window.
  * Uploaded files. They live in the api-uploads volume (STORAGE_DRIVER=local)
    or in R2. Back those up separately or every image restores as a dead
    reference.
  * Keycloak. Accounts and credentials are in its own Postgres. Restore Mongo
    without it and the boards exist but nobody can sign in to open them: ACLs
    key on the Keycloak subject id.

RESTORE:

    qomranote restore --from ./backups/<archive> --confirm RESTORE

Restore is destructive by design: it drops the target collections first, because
merging a backup into a half-surviving database produces a state nobody can
reason about. Test it against a scratch MONGO_DB before you need it — an
untested backup is a hypothesis, not a backup.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, log, err := bootstrap()
		if err != nil {
			return err
		}
		bin, err := exec.LookPath(backupOpts.tool)
		if err != nil {
			return ErrToolMissing
		}
		if err := os.MkdirAll(backupOpts.dir, 0o700); err != nil {
			return fmt.Errorf("backup directory: %w", err)
		}
		archive := filepath.Join(backupOpts.dir, ArchiveName(cfg.MongoDB, time.Now()))

		ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Minute)
		defer cancel()
		run := exec.CommandContext(ctx, bin, BackupArgs(cfg.MongoURI, cfg.MongoDB, archive)...)
		run.Stdout, run.Stderr = os.Stdout, os.Stderr
		if err := run.Run(); err != nil {
			// Remove the stub so a failed dump never sits in the directory
			// looking like a backup. A zero-byte archive that retention later
			// counts as "one of the fourteen we keep" is how a backup system
			// silently becomes no backup system.
			_ = os.Remove(archive)
			return fmt.Errorf("mongodump: %w", err)
		}
		info, err := os.Stat(archive)
		if err != nil || info.Size() == 0 {
			_ = os.Remove(archive)
			return fmt.Errorf("mongodump reported success but wrote no archive to %s", archive)
		}
		log.Info("backup written",
			zap.String("archive", archive),
			zap.Int64("bytes", info.Size()),
			zap.String("db", cfg.MongoDB))

		if names, lerr := archivesIn(backupOpts.dir, cfg.MongoDB); lerr == nil {
			for _, old := range PruneList(names, backupOpts.keep) {
				if rerr := os.Remove(filepath.Join(backupOpts.dir, old)); rerr != nil {
					log.Warn("could not prune an old archive", zap.String("archive", old), zap.Error(rerr))
					continue
				}
				log.Info("pruned an archive past the retention window", zap.String("archive", old))
			}
		}

		// Printed on every success, not buried in --help. An operator who
		// believes the archive is complete is worse off than one who knows what
		// is still unprotected.
		fmt.Printf(`
Backup complete: %s

Still unprotected by this archive:
  * uploaded files  — the api-uploads volume, or your R2 bucket
  * Keycloak        — accounts and credentials, in its own Postgres
  * anything written since this dump — MongoDB is a standalone node here, so
    there is no oplog and no point-in-time recovery

Restore with:
  qomranote restore --from %s --confirm RESTORE
`, archive, archive)
		return nil
	},
}

var restoreOpts struct {
	from    string
	confirm string
	tool    string
}

var restoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "Restore the MongoDB database from a backup archive (DESTRUCTIVE)",
	Long: `Restore MONGO_DB from an archive written by 'qomranote backup'.

This DROPS the collections it restores before writing them. That is deliberate:
merging an archive into a database that still holds rows leaves both the
survivors and the backup's older copies of the same documents, which is worse
than either and impossible to reason about during an incident.

Requires --confirm RESTORE, mirroring the typed confirmation the account-deletion
endpoint asks for. Check MONGO_URI and MONGO_DB before running it; the safest
first use is against a scratch database, and a backup nobody has ever restored is
a hypothesis rather than a backup.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, log, err := bootstrap()
		if err != nil {
			return err
		}
		if restoreOpts.from == "" {
			return errors.New("restore: --from <archive> is required")
		}
		if restoreOpts.confirm != "RESTORE" {
			return fmt.Errorf(
				"restore would DROP and overwrite every collection in %q at %s — "+
					"re-run with --confirm RESTORE if that is what you want",
				cfg.MongoDB, cfg.MongoURI)
		}
		if _, err := os.Stat(restoreOpts.from); err != nil {
			return fmt.Errorf("restore: %w", err)
		}
		bin, err := exec.LookPath(restoreOpts.tool)
		if err != nil {
			return ErrToolMissing
		}

		ctx, cancel := context.WithTimeout(cmd.Context(), 120*time.Minute)
		defer cancel()
		run := exec.CommandContext(ctx, bin, RestoreArgs(cfg.MongoURI, cfg.MongoDB, restoreOpts.from)...)
		run.Stdout, run.Stderr = os.Stdout, os.Stderr
		if err := run.Run(); err != nil {
			return fmt.Errorf("mongorestore: %w", err)
		}
		log.Info("restore complete", zap.String("archive", restoreOpts.from), zap.String("db", cfg.MongoDB))

		// The indexes are not optional afterwards. mongorestore replays the ones
		// captured in the archive, but a restore into a database whose schema has
		// moved on since the dump is exactly when the ensure step matters, and
		// forgetting it leaves a workspace that is correct and unusably slow.
		fmt.Println(`
Restore complete. Now:
  1. qomranote migrate     — re-ensure indexes against the CURRENT schema
  2. restore uploaded files (api-uploads volume, or the R2 bucket)
  3. confirm Keycloak still holds the same subject ids, or every ACL points at
     accounts that no longer exist`)
		return nil
	},
}

func init() {
	backupCmd.Flags().StringVar(&backupOpts.dir, "out", backupOpts.dir, "directory to write archives into")
	backupCmd.Flags().IntVar(&backupOpts.keep, "keep", backupOpts.keep, "how many archives to retain (0 keeps everything)")
	backupCmd.Flags().StringVar(&backupOpts.tool, "tool", backupOpts.tool, "mongodump binary to use")

	restoreCmd.Flags().StringVar(&restoreOpts.from, "from", "", "archive to restore (required)")
	restoreCmd.Flags().StringVar(&restoreOpts.confirm, "confirm", "", `must be the literal "RESTORE"`)
	restoreCmd.Flags().StringVar(&restoreOpts.tool, "tool", "mongorestore", "mongorestore binary to use")
}
