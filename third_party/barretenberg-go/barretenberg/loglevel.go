package barretenberg

/*
// bb's global log level (barretenberg/common/log.cpp): set at load time to 4
// (info), or 5 (verbose) when BB_VERBOSE=1. info() prints at >= 4.
extern int bb_log_level;
static void earth_bb_set_log_level(int level) { bb_log_level = level; }
static int earth_bb_log_level(void) { return bb_log_level; }
*/
import "C"

// Barretenberg log levels as its log.cpp compares them.
const (
	LogLevelWarn    = 3 // below info: the verifier's per-proof info lines are silenced
	LogLevelInfo    = 4 // bb's default
	LogLevelVerbose = 5 // BB_VERBOSE=1
)

// SetLogLevel sets Barretenberg's global log level. Not synchronized with
// running verifications: call it once at start-up, before any.
func SetLogLevel(level int) { C.earth_bb_set_log_level(C.int(level)) }

// LogLevel is Barretenberg's global log level.
func LogLevel() int { return int(C.earth_bb_log_level()) }
