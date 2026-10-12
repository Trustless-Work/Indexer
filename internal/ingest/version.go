package ingest

// Version identifies the running build in /status. Stamped at build time
// by the Dockerfile's -X ldflag (which targets exactly this symbol:
// github.com/Trustless-Work/Indexer/internal/ingest.Version); unstamped
// builds report "dev".
var Version = "dev"
