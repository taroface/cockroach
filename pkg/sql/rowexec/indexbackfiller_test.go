// Copyright 2024 The Cockroach Authors.
//
// Use of this software is governed by the CockroachDB Software License
// included in the /LICENSE file.

package rowexec

import (
	"context"
	"testing"
	"time"

	"github.com/cockroachdb/cockroach/pkg/base"
	"github.com/cockroachdb/cockroach/pkg/security/username"
	"github.com/cockroachdb/cockroach/pkg/sql/isql"
	"github.com/cockroachdb/cockroach/pkg/sql/sqlerrors"
	"github.com/cockroachdb/cockroach/pkg/testutils/serverutils"
	"github.com/cockroachdb/cockroach/pkg/util/leaktest"
	"github.com/cockroachdb/cockroach/pkg/util/log"
	"github.com/cockroachdb/cockroach/pkg/util/mon"
	"github.com/cockroachdb/cockroach/pkg/util/retry"
	"github.com/stretchr/testify/require"
)

func TestRetryOfIndexEntryBatch(t *testing.T) {
	defer leaktest.AfterTest(t)()
	defer log.Scope(t).Close(t)

	ctx := context.Background()
	srv := serverutils.StartServerOnly(t, base.TestServerArgs{})
	defer srv.Stopper().Stop(ctx)
	db := srv.SystemLayer().InternalDB().(isql.DB)

	const initialChunkSize int64 = 50000
	oomErr := mon.NewMemoryBudgetExceededError(1, 1, 1)
	nonOomErr := sqlerrors.NewUndefinedUserError(username.NodeUserName())

	for _, tc := range []struct {
		desc              string
		errs              []error
		retryErr          error
		expectedErr       error
		expectedChunkSize int64
	}{
		{"happy-path", nil, nil, nil, initialChunkSize},
		{"retry-once", []error{oomErr}, nil, nil, initialChunkSize >> 1},
		{"retry-then-fail", []error{oomErr, oomErr, nonOomErr}, nil, nonOomErr, initialChunkSize >> 2},
		{"retry-exhaustive", []error{oomErr, oomErr, oomErr, oomErr}, nil, oomErr, initialChunkSize >> 3},
		{"retry-error", []error{oomErr}, nonOomErr, oomErr, initialChunkSize},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			i := 0
			br := indexBatchRetry{
				nextChunkSize: initialChunkSize,
				retryOpts: retry.Options{
					InitialBackoff: 2 * time.Millisecond,
					Multiplier:     2,
					MaxRetries:     2,
					MaxBackoff:     10 * time.Millisecond,
				},
				buildIndexChunk: func(ctx context.Context, txn isql.Txn) error {
					if i < len(tc.errs) {
						return tc.errs[i]
					}
					return nil
				},
				resetForNextAttempt: func(ctx context.Context) error {
					i++
					return tc.retryErr
				},
			}
			err := br.buildBatchWithRetry(ctx, db)
			if tc.expectedErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.expectedErr)
			}
			require.Equal(t, tc.expectedChunkSize, br.nextChunkSize)
		})
	}
}
