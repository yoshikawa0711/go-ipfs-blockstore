package blockstore

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	blocks "github.com/ipfs/go-block-format"
	cid "github.com/ipfs/go-cid"
	ds "github.com/ipfs/go-datastore"
	delaystore "github.com/ipfs/go-datastore/delayed"
	syncds "github.com/ipfs/go-datastore/sync"
	delay "github.com/ipfs/go-ipfs-delay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var exampleBlock = blocks.NewBlock([]byte("foo"))

func testArcCached(ctx context.Context, bs Blockstore) (*arccache, error) {
	if ctx == nil {
		ctx = context.TODO()
	}
	opts := DefaultCacheOpts()
	opts.HasBloomFilterSize = 0
	opts.HasBloomFilterHashes = 0
	bbs, err := CachedBlockstore(ctx, bs, opts)
	if err == nil {
		return bbs.(*arccache), nil
	}
	return nil, err
}

func createStores(t testing.TB) (*arccache, Blockstore, *callbackDatastore) {
	cd := &callbackDatastore{f: func() {}, ds: ds.NewMapDatastore()}
	bs := NewBlockstore(syncds.MutexWrap(cd))
	arc, err := testArcCached(context.TODO(), bs)
	if err != nil {
		t.Fatal(err)
	}
	return arc, bs, cd
}

func createStoresWithDelay(t testing.TB, delayed delay.D) (*arccache, Blockstore, *callbackDatastore) {
	cd := &callbackDatastore{f: func() {}, ds: ds.NewMapDatastore()}
	slowStore := delaystore.New(cd, delayed)
	bs := NewBlockstore(syncds.MutexWrap(slowStore))
	arc, err := testArcCached(context.TODO(), bs)
	if err != nil {
		t.Fatal(err)
	}
	return arc, bs, cd
}

func trap(message string, cd *callbackDatastore, t *testing.T) {
	cd.SetFunc(func() {
		t.Fatal(message)
	})
}
func untrap(cd *callbackDatastore) {
	cd.SetFunc(func() {})
}

func TestRemoveCacheEntryOnDelete(t *testing.T) {
	arc, _, cd := createStores(t)

	arc.Put(exampleBlock)

	cd.Lock()
	writeHitTheDatastore := false
	cd.Unlock()

	cd.SetFunc(func() {
		writeHitTheDatastore = true
	})

	arc.DeleteBlock(exampleBlock.Cid())
	arc.Put(exampleBlock)
	if !writeHitTheDatastore {
		t.Fail()
	}
}

func TestElideDuplicateWrite(t *testing.T) {
	arc, _, cd := createStores(t)

	arc.Put(exampleBlock)
	trap("write hit datastore", cd, t)
	arc.Put(exampleBlock)
}

func TestHasRequestTriggersCache(t *testing.T) {
	arc, _, cd := createStores(t)

	arc.Has(exampleBlock.Cid())
	trap("has hit datastore", cd, t)
	if has, err := arc.Has(exampleBlock.Cid()); has || err != nil {
		t.Fatal("has was true but there is no such block")
	}

	untrap(cd)
	err := arc.Put(exampleBlock)
	if err != nil {
		t.Fatal(err)
	}

	trap("has hit datastore", cd, t)

	if has, err := arc.Has(exampleBlock.Cid()); !has || err != nil {
		t.Fatal("has returned invalid result")
	}
}

func TestGetFillsCache(t *testing.T) {
	arc, _, cd := createStores(t)

	if bl, err := arc.Get(exampleBlock.Cid()); bl != nil || err == nil {
		t.Fatal("block was found or there was no error")
	}

	trap("has hit datastore", cd, t)

	if has, err := arc.Has(exampleBlock.Cid()); has || err != nil {
		t.Fatal("has was true but there is no such block")
	}
	if _, err := arc.GetSize(exampleBlock.Cid()); err != ErrNotFound {
		t.Fatal("getsize was true but there is no such block")
	}

	untrap(cd)

	if err := arc.Put(exampleBlock); err != nil {
		t.Fatal(err)
	}

	trap("has hit datastore", cd, t)

	if has, err := arc.Has(exampleBlock.Cid()); !has || err != nil {
		t.Fatal("has returned invalid result")
	}
	if blockSize, err := arc.GetSize(exampleBlock.Cid()); blockSize == -1 || err != nil {
		t.Fatal("getsize returned invalid result", blockSize, err)
	}
}

func TestGetAndDeleteFalseShortCircuit(t *testing.T) {
	arc, _, cd := createStores(t)

	arc.Has(exampleBlock.Cid())
	arc.GetSize(exampleBlock.Cid())

	trap("get hit datastore", cd, t)

	if bl, err := arc.Get(exampleBlock.Cid()); bl != nil || err != ErrNotFound {
		t.Fatal("get returned invalid result")
	}

	if arc.DeleteBlock(exampleBlock.Cid()) != nil {
		t.Fatal("expected deletes to be idempotent")
	}
}

func TestArcCreationFailure(t *testing.T) {
	if arc, err := newARCCachedBS(context.TODO(), nil, -1); arc != nil || err == nil {
		t.Fatal("expected error and no cache")
	}
}

func TestInvalidKey(t *testing.T) {
	arc, _, _ := createStores(t)

	bl, err := arc.Get(cid.Cid{})

	if bl != nil {
		t.Fatal("blocks should be nil")
	}
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHasAfterSucessfulGetIsCached(t *testing.T) {
	arc, bs, cd := createStores(t)

	bs.Put(exampleBlock)

	arc.Get(exampleBlock.Cid())

	trap("has hit datastore", cd, t)
	arc.Has(exampleBlock.Cid())
}

func TestGetSizeAfterSucessfulGetIsCached(t *testing.T) {
	arc, bs, cd := createStores(t)

	bs.Put(exampleBlock)

	arc.Get(exampleBlock.Cid())

	trap("has hit datastore", cd, t)
	arc.GetSize(exampleBlock.Cid())
}

func TestGetSizeAfterSucessfulHas(t *testing.T) {
	arc, bs, _ := createStores(t)

	bs.Put(exampleBlock)
	has, err := arc.Has(exampleBlock.Cid())
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("expected to have block")
	}

	if size, err := arc.GetSize(exampleBlock.Cid()); err != nil {
		t.Fatal(err)
	} else if size != len(exampleBlock.RawData()) {
		t.Fatalf("expected size %d, got %d", len(exampleBlock.RawData()), size)
	}
}

func TestGetSizeMissingZeroSizeBlock(t *testing.T) {
	arc, bs, cd := createStores(t)
	emptyBlock := blocks.NewBlock([]byte{})
	missingBlock := blocks.NewBlock([]byte("missingBlock"))

	bs.Put(emptyBlock)

	arc.Get(emptyBlock.Cid())

	trap("has hit datastore", cd, t)
	if blockSize, err := arc.GetSize(emptyBlock.Cid()); blockSize != 0 || err != nil {
		t.Fatal("getsize returned invalid result")
	}
	untrap(cd)

	arc.Get(missingBlock.Cid())

	trap("has hit datastore", cd, t)
	if _, err := arc.GetSize(missingBlock.Cid()); err != ErrNotFound {
		t.Fatal("getsize returned invalid result")
	}
}

func TestDifferentKeyObjectsWork(t *testing.T) {
	arc, bs, cd := createStores(t)

	bs.Put(exampleBlock)

	arc.Get(exampleBlock.Cid())

	trap("has hit datastore", cd, t)
	cidstr := exampleBlock.Cid().String()

	ncid, err := cid.Decode(cidstr)
	if err != nil {
		t.Fatal(err)
	}

	arc.Has(ncid)
}

func TestPutManyCaches(t *testing.T) {
	arc, _, cd := createStores(t)
	arc.PutMany([]blocks.Block{exampleBlock})

	trap("has hit datastore", cd, t)
	arc.Has(exampleBlock.Cid())
	arc.GetSize(exampleBlock.Cid())
	untrap(cd)
	arc.DeleteBlock(exampleBlock.Cid())

	arc.Put(exampleBlock)
	trap("PunMany has hit datastore", cd, t)
	arc.PutMany([]blocks.Block{exampleBlock})
}

func init() {
	rand.Seed(time.Now().UnixNano())
}

func Benchmark_SimplePutGet(b *testing.B) {
	arc, _, _ := createStores(b)

	trace := make([]blocks.Block, b.N)
	for i := 0; i < b.N; i++ {
		token := make([]byte, 4)
		rand.Read(token)
		trace[i] = blocks.NewBlock(token)
	}
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if i%2 == 0 {
			require.NoError(b, arc.Put(trace[i]))
		}
	}

	for i := 0; i < b.N; i++ {
		_, err := arc.Get(trace[i].Cid())
		if i%2 == 0 {
			assert.NoError(b, err)
		}
	}
}

func Benchmark_SimplePutDelete(b *testing.B) {
	arc, _, _ := createStores(b)

	trace := make([]blocks.Block, b.N)
	for i := 0; i < b.N; i++ {
		token := make([]byte, 4)
		rand.Read(token)
		trace[i] = blocks.NewBlock(token)
	}
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		require.NoError(b, arc.Put(trace[i]))
	}

	for i := 0; i < b.N; i++ {
		err := arc.DeleteBlock(trace[i].Cid())
		require.NoError(b, err)
	}
}

func Benchmark_ThrashPut(b *testing.B) {
	table := []struct {
		numBlocks int
		threads   int
		delay     time.Duration
	}{
		{
			numBlocks: 1_000_000,
			threads:   1,
			delay:     time.Millisecond * 1,
		},
		{
			numBlocks: 1_000_000,
			threads:   32,
			delay:     time.Millisecond * 1,
		},
		{
			numBlocks: 1_000_000,
			threads:   64,
			delay:     time.Millisecond * 1,
		},
		{
			numBlocks: 1_000_000,
			threads:   500,
			delay:     time.Millisecond * 1,
		},
	}

	for _, test := range table {
		b.Run(fmt.Sprintf("%d_threads-%d_blocks", test.threads, test.numBlocks), func(b *testing.B) {
			arc, _, _ := createStoresWithDelay(b, delay.Fixed(test.delay))
			trace := make([]blocks.Block, test.numBlocks)
			for i := 0; i < test.numBlocks; i++ {
				token := make([]byte, 4)
				rand.Read(token)
				trace[i] = blocks.NewBlock(token)
			}

			for i := 0; i < test.threads; i++ {
				go func() {
					for {
						idx := rand.Intn(test.numBlocks - 1)
						arc.Put(trace[idx])
					}
				}()

				go func() {
					for {
						idx := rand.Intn(test.numBlocks - 1)
						arc.Get(trace[idx].Cid())
					}
				}()

				go func() {
					for {
						idx := rand.Intn(test.numBlocks - 1)
						arc.DeleteBlock(trace[idx].Cid())
					}
				}()
			}

			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				require.NoError(b, arc.Put(trace[i]))
			}

		})
	}
}

func Benchmark_ThrashGet(b *testing.B) {
	table := []struct {
		numBlocks int
		threads   int
		delay     time.Duration
	}{
		{
			numBlocks: 1_000_000,
			threads:   1,
			delay:     time.Millisecond * 1,
		},
		{
			numBlocks: 1_000_000,
			threads:   32,
			delay:     time.Millisecond * 1,
		},
		{
			numBlocks: 1_000_000,
			threads:   64,
			delay:     time.Millisecond * 1,
		},
		{
			numBlocks: 1_000_000,
			threads:   500,
			delay:     time.Millisecond * 1,
		},
	}

	for _, test := range table {
		b.Run(fmt.Sprintf("%d_threads-%d_blocks", test.threads, test.numBlocks), func(b *testing.B) {
			arc, _, _ := createStoresWithDelay(b, delay.Fixed(test.delay))
			trace := make([]blocks.Block, test.numBlocks)
			for i := 0; i < test.numBlocks; i++ {
				token := make([]byte, 4)
				rand.Read(token)
				trace[i] = blocks.NewBlock(token)
			}

			for i := 0; i < test.threads; i++ {
				go func() {
					for {
						idx := rand.Intn(test.numBlocks - 1)
						arc.Put(trace[idx])
					}
				}()

				go func() {
					for {
						idx := rand.Intn(test.numBlocks - 1)
						arc.Get(trace[idx].Cid())
					}
				}()

				go func() {
					for {
						idx := rand.Intn(test.numBlocks - 1)
						arc.DeleteBlock(trace[idx].Cid())
					}
				}()
			}

			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				arc.Get(trace[i].Cid())
			}

		})
	}
}

func Benchmark_ThrashDelete(b *testing.B) {
	table := []struct {
		numBlocks int
		threads   int
		delay     time.Duration
	}{
		{
			numBlocks: 1_000_000,
			threads:   1,
			delay:     time.Millisecond * 1,
		},
		{
			numBlocks: 1_000_000,
			threads:   32,
			delay:     time.Millisecond * 1,
		},
		{
			numBlocks: 1_000_000,
			threads:   64,
			delay:     time.Millisecond * 1,
		},
		{
			numBlocks: 1_000_000,
			threads:   500,
			delay:     time.Millisecond * 1,
		},
	}

	for _, test := range table {
		b.Run(fmt.Sprintf("%d_threads-%d_blocks", test.threads, test.numBlocks), func(b *testing.B) {
			arc, _, _ := createStoresWithDelay(b, delay.Fixed(test.delay))
			trace := make([]blocks.Block, test.numBlocks)
			for i := 0; i < test.numBlocks; i++ {
				token := make([]byte, 4)
				rand.Read(token)
				trace[i] = blocks.NewBlock(token)
			}

			for i := 0; i < test.threads; i++ {
				go func() {
					for {
						idx := rand.Intn(test.numBlocks - 1)
						arc.Put(trace[idx])
					}
				}()

				go func() {
					for {
						idx := rand.Intn(test.numBlocks - 1)
						arc.Get(trace[idx].Cid())
					}
				}()

				go func() {
					for {
						idx := rand.Intn(test.numBlocks - 1)
						arc.DeleteBlock(trace[idx].Cid())
					}
				}()
			}

			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				arc.DeleteBlock(trace[i].Cid())
			}

		})
	}
}
