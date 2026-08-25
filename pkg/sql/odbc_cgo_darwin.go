//go:build cgo && odbc

package sql

// Provide the preloaded-symbols table that static libltdl expects.
// When unixODBC is linked statically on macOS, lt_dlinit looks for this symbol;
// an empty table (NULL-terminated) satisfies it without loading any modules at startup.

/*
typedef struct { const char *name; void *address; } lt_dlsymlist;
lt_dlsymlist lt_libltdlc_LTX_preloaded_symbols[] = { {0, 0} };
*/
import "C"
