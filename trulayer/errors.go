package trulayer

import "errors"

// ErrAPIKeyRequired is returned by NewClient when no API key is provided
// and dry-run mode is not enabled.
var ErrAPIKeyRequired = errors.New("trulayer: api key is required (set TRULAYER_DRY_RUN=true to disable)")
