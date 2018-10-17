package promql_test

import (
	"context"
	"time"

	"code.cloudfoundry.org/go-loggregator/rpc/loggregator_v2"
	logcache "code.cloudfoundry.org/log-cache/client"
	"code.cloudfoundry.org/log-cache/internal/pkg/promql"
	"code.cloudfoundry.org/log-cache/rpc/logcache_v1"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("WalkingDataReader", func() {
	var (
		spyLogCache *spyLogCache
		r           *promql.WalkingDataReader
	)

	BeforeEach(func() {
		spyLogCache = newSpyLogCache()
		r = promql.NewWalkingDataReader(spyLogCache.Read)
	})

	It("returns the error from the context", func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := r.Read(ctx, &logcache_v1.ReadRequest{})
		Expect(err).To(HaveOccurred())
	})
})

type spyLogCache struct {
	results []*loggregator_v2.Envelope
	err     error
}

func newSpyLogCache() *spyLogCache {
	return &spyLogCache{}
}

func (s *spyLogCache) Read(
	ctx context.Context,
	sourceID string,
	start time.Time,
	opts ...logcache.ReadOption,
) ([]*loggregator_v2.Envelope, error) {
	return s.results, s.err
}
