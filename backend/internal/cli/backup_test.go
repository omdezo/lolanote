package cli

import (
	"strings"
	"testing"
	"time"
)

// OPS1 — the product shipped with no backup at all: one docker volume, on one
// host, that `docker compose down -v` removes without a prompt.
//
// These assert the parts that are decisions rather than plumbing. The mongodump
// invocation is one of them: --archive+--gzip because a single file is what an
// operator can checksum and copy off the host, and NO --oplog because the
// deployment is a standalone node and passing it would fail at the worst
// possible moment. Retention is another: a `keep` that can be misconfigured into
// deleting everything is a backup system that occasionally becomes the disaster.

func TestBackupArgs_SingleGzippedArchiveAndNoOplog(t *testing.T) {
	got := strings.Join(BackupArgs("mongodb://mongo:27017", "qomranote", "/backups/x.archive.gz"), " ")
	for _, want := range []string{
		"--uri=mongodb://mongo:27017",
		"--db=qomranote",
		"--archive=/backups/x.archive.gz",
		"--gzip",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("mongodump args %q are missing %q", got, want)
		}
	}
	// --oplog needs a replica set. This deployment has none, so asking for it
	// turns every backup into a failure discovered during the first restore.
	if strings.Contains(got, "--oplog") {
		t.Error("--oplog is passed, and this MongoDB is a standalone node: the dump would fail")
	}
}

func TestRestoreArgs_DropsBeforeWritingAndStaysInsideOneDatabase(t *testing.T) {
	got := strings.Join(RestoreArgs("mongodb://mongo:27017", "qomranote", "/backups/x.archive.gz"), " ")
	if !strings.Contains(got, "--drop") {
		t.Error("no --drop: a restore that merges into surviving rows leaves both the " +
			"survivors and the backup's older copies of the same _ids")
	}
	if !strings.Contains(got, "--nsInclude=qomranote.*") {
		t.Errorf("args %q do not confine the restore to the configured database", got)
	}
}

// One host can hold staging and production, and an operator reading `ls` needs
// the order to be the order things happened in — in UTC, not in whatever zone
// the machine is set to.
func TestArchiveName_IsSortableAndNamesTheDatabase(t *testing.T) {
	early := ArchiveName("qomranote", time.Date(2026, 7, 30, 23, 0, 0, 0, time.UTC))
	late := ArchiveName("qomranote", time.Date(2026, 7, 31, 1, 0, 0, 0, time.UTC))
	if !(early < late) {
		t.Fatalf("%q does not sort before %q; retention sorts by name", early, late)
	}
	if !strings.HasPrefix(early, "qomranote-") || !strings.HasSuffix(early, ".archive.gz") {
		t.Fatalf("archive name %q does not carry the database or the format", early)
	}
	// Same instant, different zones, same name.
	zone := time.FixedZone("GST", 4*3600)
	if a, b := ArchiveName("qomranote", time.Date(2026, 7, 30, 23, 0, 0, 0, time.UTC)),
		ArchiveName("qomranote", time.Date(2026, 7, 31, 3, 0, 0, 0, zone)); a != b {
		t.Fatalf("the same instant produced %q and %q; the name must not depend on the host's timezone", a, b)
	}
}

func TestPruneList_KeepsTheNewestAndNeverEmptiesTheDirectory(t *testing.T) {
	names := []string{
		"qomranote-20260728T010000Z.archive.gz",
		"qomranote-20260731T010000Z.archive.gz",
		"qomranote-20260729T010000Z.archive.gz",
		"qomranote-20260730T010000Z.archive.gz",
	}
	got := PruneList(names, 2)
	if len(got) != 2 {
		t.Fatalf("pruned %v, want the two oldest", got)
	}
	for _, kept := range []string{"qomranote-20260731T010000Z.archive.gz", "qomranote-20260730T010000Z.archive.gz"} {
		for _, g := range got {
			if g == kept {
				t.Fatalf("%s is one of the two newest and was pruned", kept)
			}
		}
	}
	if got := PruneList(names, 0); got != nil {
		t.Fatalf("keep=0 pruned %v; a retention setting must never be able to mean 'delete everything'", got)
	}
	if got := PruneList(names, -1); got != nil {
		t.Fatalf("keep=-1 pruned %v", got)
	}
	if got := PruneList(names, 10); got != nil {
		t.Fatalf("keep above the archive count pruned %v", got)
	}
}

// The help text is the runbook. If it stops naming what the archive does NOT
// cover, an operator restores Mongo, finds every board intact, and only then
// discovers that no one can log in and every image is a dead reference.
func TestBackupHelp_NamesWhatItDoesNotProtect(t *testing.T) {
	// Case-insensitive: the assertion is about the CONCEPT still being named,
	// not about where in a sentence the word happens to fall.
	long := strings.ToLower(backupCmd.Long)
	for _, must := range []string{"standalone", "point-in-time", "uploaded files", "keycloak"} {
		if !strings.Contains(long, must) {
			t.Errorf("backup --help no longer mentions %q; that is the whole value of the text", must)
		}
	}
	if !strings.Contains(restoreCmd.Long, "--confirm RESTORE") {
		t.Error("restore --help does not state the confirmation it requires")
	}
}

// The one prerequisite, and the way around it. The API image deliberately does
// not ship mongodb-tools — a database client inside the API container widens
// what a compromised API can reach — so the error has to say where to run it
// instead, or the feature reads as broken.
func TestToolMissingError_NamesTheFix(t *testing.T) {
	msg := ErrToolMissing.Error()
	for _, must := range []string{"mongodb-database-tools", "docker run", "mongo:7"} {
		if !strings.Contains(msg, must) {
			t.Errorf("the missing-tool error does not mention %q: %s", must, msg)
		}
	}
}
