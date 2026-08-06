package cli

import "testing"

// The member host is the one value in replSetInitiate that cannot be corrected
// afterwards from outside: a set that records itself as "localhost:27017" is
// reachable only from inside the container that initiated it, and every other
// client — the API, a backup, a shell — then fails to find a primary against a
// database that is running perfectly well. So it is read from the URI this
// process actually reached mongod on, and falls back to the compose service
// name rather than to anything loopback-shaped.
func TestReplicaMemberHost(t *testing.T) {
	cases := []struct {
		name string
		uri  string
		want string
	}{
		{"compose service name", "mongodb://mongo:27017", "mongo:27017"},
		{"carries the options", "mongodb://mongo:27017/?replicaSet=rs0", "mongo:27017"},
		{"carries a database", "mongodb://mongo:27017/qomranote", "mongo:27017"},
		{"port implied", "mongodb://mongo", "mongo:27017"},
		{"credentials stripped", "mongodb://user:p%40ss@db.internal:27018/?replicaSet=rs0", "db.internal:27018"},
		{"seed list is not one member", "mongodb://a:27017,b:27017/?replicaSet=rs0", defaultReplicaMemberHost},
		{"srv is never ours to initiate", "mongodb+srv://cluster0.example.mongodb.net/", defaultReplicaMemberHost},
		{"unreadable", "", defaultReplicaMemberHost},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := replicaMemberHost(tc.uri); got != tc.want {
				t.Fatalf("replicaMemberHost(%q) = %q, want %q", tc.uri, got, tc.want)
			}
		})
	}
}
