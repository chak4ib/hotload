package integrationtests

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/infobloxopen/hotload"
	_ "github.com/infobloxopen/hotload/fsnotify"
	"github.com/infobloxopen/hotload/modtime"
	"github.com/lib/pq"
	_ "github.com/lib/pq"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

const (
	fsnotifyStrategy = "fsnotify"
	configPath       = "/var/config.txt"
	hotloadTestDsn   = "postgresql://admin:test@hotload-integration-tests-postgresql.default.svc.cluster.local:5432/hotload_test?sslmode=disable"
	hotloadTest1Dsn  = "postgresql://admin:test@hotload-integration-tests-postgresql.default.svc.cluster.local:5432/hotload_test1?sslmode=disable"
	testSqlSetup     = "CREATE TABLE test (c1 int)"
)

var (
	ctx         context.Context
	cancelCtxFn context.CancelFunc
	mtm         *modtime.ModTimeMonitor
)

func init() {
	// this function call registers the lib/pq postgres driver with hotload
	hotload.RegisterSQLDriver("postgres", &pq.Driver{})
}

func setDSN(dsn string, path string) {
	err := os.WriteFile(path, []byte(dsn), 777)
	if err != nil {
		Fail("error writing dsn file")
	}
	// Yield thread to let switch over take place
	time.Sleep(250 * time.Millisecond)
}

// Open a db or die
func openDb(dsn string) *sql.DB {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		Fail(fmt.Sprintf("error opening db: %v", err))
	}

	return db
}

func expectValueInDb(db *sql.DB, expected int64) {
	r, err := db.Query("SELECT c1 FROM test")
	var c1 int64
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("error reading from table test: %v", err))
	res := r.Next()
	Expect(res).To(BeTrue(), "no rows found")
	err = r.Scan(&c1)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("error calling r.Scan(): %v", err))
	Expect(c1).To(Equal(expected))
}

var _ = Describe("hotload integration tests", func() {
	BeforeSuite(func() {
		// create tables and chairs
		hlt, err := sql.Open("postgres", hotloadTestDsn)
		hlt1, err := sql.Open("postgres", hotloadTest1Dsn)

		for {
			time.Sleep(5 * time.Second)
			_, err = hlt.Exec(testSqlSetup)
			if err != nil {
				log.Printf("error creating test table in hlt: %v", err)
				continue
				//Fail(fmt.Sprintf()
			}

			_, err = hlt1.Exec(testSqlSetup)
			if err != nil {
				log.Printf("error creating test table in hlt1: %v", err)
				continue
			}

			break
		}

		// enable ModTimeMonitor to monitor configPath
		ctx, cancelCtxFn = context.WithCancel(context.Background())
		mtm = modtime.NewModTimeMonitor(ctx,
			// note the check-interval must be shorter than the sleep interval in setDSN()
			modtime.WithCheckInterval(100*time.Millisecond),
			modtime.WithLogger(func(args ...any) {
				log.Println(args...)
			}),
		)
		mtm.AddMonitoredPath(fsnotifyStrategy, configPath)
	}, 240)

	AfterSuite(func() {
		cancelCtxFn()
		time.Sleep(200 * time.Millisecond)
	}, 240)

	var (
		db     *sql.DB
		hltDb  *sql.DB
		hlt1Db *sql.DB
	)

	BeforeEach(func() {
		setDSN(hotloadTestDsn, configPath)
		hltDb = openDb(hotloadTestDsn)
		hlt1Db = openDb(hotloadTest1Dsn)
		newDb, err := sql.Open("hotload", "fsnotify://postgres"+configPath)
		if err != nil {
			Fail(fmt.Sprintf("error opening db: %v", err))
		}

		db = newDb

		err = db.Ping()
		if err != nil {
			Fail(fmt.Sprintf("err pinging db: %v", err))
		}
	})

	It("should connect to new db when file changes", func() {
		var prevModTime time.Time
		for i := 0; i < 2; i++ {
			// Get configPath modtime
			sts, err := mtm.GetPathStatus(fsnotifyStrategy, configPath)
			if err != nil {
				Fail(fmt.Sprintf("GetPathStatus(%s) err: %v", configPath, err))
			}
			log.Println(fmt.Sprintf("GetPathStatus(%s): %v", configPath, sts))

			// Verify configPath modtime was updated after configPath was updated with new DSN
			if sts.ModTime.After(prevModTime) {
				prevModTime = sts.ModTime
			} else {
				Fail(fmt.Sprintf("%s: new sts.ModTime(%s) <= prevModTime(%s)", configPath, sts.ModTime, prevModTime))
			}

			r, err := db.Exec(fmt.Sprintf("INSERT INTO test (c1) VALUES (%d)", i))
			if err != nil {
				Fail(fmt.Sprintf("error inserting row: %v", err))
			}
			log.Print(r)

			// Set new DSN, note that this sleeps for 250 millisecs
			setDSN(hotloadTest1Dsn, configPath)
		}
		expectValueInDb(hltDb, 0)
		expectValueInDb(hlt1Db, 1)
	})
})
