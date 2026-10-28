// Spike 0b: De-risk Massive (ex-Polygon.io) FLAT FILES dependency.
//
// Goal: confirm that, under this account's REAL-TIME entitlement, we can pull
// whole-market MINUTE aggregates as S3-compatible flat files. These files seed
// the RVOL baseline (intraday cumulative-by-time-of-day, 192 five-minute buckets
// anchored at 04:00 ET). This spike proves the files exist, are readable, carry
// the expected columns, and -- critically -- include EXTENDED-HOURS rows
// (pre-09:30 ET), which the 04:00 anchor depends on.
//
// Research summary (see findings returned by the workflow):
//   - Polygon.io rebranded to Massive on 2025-10-30. api/socket/files.polygon.io
//     remain supported in parallel with *.massive.com; full backward compat.
//   - Flat files = S3-compatible object storage, bucket "flatfiles".
//   - Per-user S3 Access Key ID + Secret from the dashboard (separate from the
//     REST/WS API key). Path style addressing, SigV4.
//   - US stocks minute aggregates path:
//     us_stocks_sip/minute_aggs_v1/YYYY/MM/YYYY-MM-DD.csv.gz
//   - CSV columns (header present): ticker,volume,open,close,high,low,window_start,transactions
//   - window_start = Unix NANOSECOND timestamp (UTC), start of the 1-min window.
//   - Daily file for previous trading day is published ~11:00 ET.
//
// Post-rebrand the current canonical host is files.massive.com; files.polygon.io
// is the legacy alias kept alive by backward-compat + 301 redirects. The host is
// configurable, and on failure the spike auto-retries the alternate host and
// tells you which one worked (the empirical answer is the real deliverable).
package main

import (
	"compress/gzip"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // embed IANA tz DB so America/New_York works on Windows w/o OS tz files

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

const (
	defaultEndpoint = "https://files.massive.com" // current canonical host (post-rebrand 2025-10-30)
	altEndpoint     = "https://files.polygon.io"  // legacy alias kept alive by backward-compat / 301
	defaultBucket   = "flatfiles"
	defaultRegion   = "us-east-1" // arbitrary but required by SigV4; the server ignores it
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "\nSPIKE FAILED: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	keyID := os.Getenv("MASSIVE_S3_KEY_ID")
	secret := os.Getenv("MASSIVE_S3_SECRET")
	if keyID == "" || secret == "" {
		return errors.New("MASSIVE_S3_KEY_ID and MASSIVE_S3_SECRET must be set " +
			"(these are the S3 Access Key ID + Secret from the Massive dashboard, " +
			"NOT the REST/WS API key)")
	}

	bucket := envOr("MASSIVE_S3_BUCKET", defaultBucket)

	// Date: workflows cannot rely on time.Now() for a "recent trading day", so we
	// require it explicitly. Accept CLI arg first, then FLATFILE_DATE env.
	date := ""
	if len(os.Args) > 1 {
		date = strings.TrimSpace(os.Args[1])
	}
	if date == "" {
		date = strings.TrimSpace(os.Getenv("FLATFILE_DATE"))
	}
	if date == "" {
		return errors.New("no date given: pass YYYY-MM-DD as the first CLI arg " +
			"or set FLATFILE_DATE. Use a RECENT WEEKDAY that is a US trading day; " +
			"the file for a given day is published ~11:00 ET the next-ish day, so " +
			"do not pick today or a holiday/weekend")
	}
	day, err := time.Parse("2006-01-02", date)
	if err != nil {
		return fmt.Errorf("FLATFILE_DATE %q is not YYYY-MM-DD: %w", date, err)
	}
	if wd := day.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return fmt.Errorf("%s is a %s; pick a weekday trading day", date, wd)
	}

	key := fmt.Sprintf("us_stocks_sip/minute_aggs_v1/%04d/%02d/%s.csv.gz",
		day.Year(), int(day.Month()), date)

	// Endpoint resolution: if MASSIVE_S3_ENDPOINT is set, use ONLY that. Otherwise
	// try the canonical massive.com host then fall back to the legacy polygon.io
	// alias, reporting which one worked.
	var endpoints []string
	if e := strings.TrimSpace(os.Getenv("MASSIVE_S3_ENDPOINT")); e != "" {
		endpoints = []string{e}
	} else {
		endpoints = []string{defaultEndpoint, altEndpoint}
	}

	fmt.Println("=== Spike 0b: Massive/Polygon flat-files reachability ===")
	fmt.Printf("bucket   : %s\n", bucket)
	fmt.Printf("object   : %s\n", key)
	fmt.Printf("date     : %s (%s)\n", date, day.Weekday())
	fmt.Println()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var lastErr error
	for i, ep := range endpoints {
		fmt.Printf("--- attempt %d/%d: endpoint=%s ---\n", i+1, len(endpoints), ep)
		client := newS3Client(keyID, secret, ep)

		if err := probeAndAnalyze(ctx, client, bucket, key); err != nil {
			lastErr = classify(err, ep)
			fmt.Printf("    failed: %v\n\n", lastErr)
			continue
		}
		fmt.Printf("\nSUCCESS via endpoint %s\n", ep)
		return nil
	}
	return fmt.Errorf("all endpoints failed; last error: %w", lastErr)
}

// newS3Client builds an S3 client pointed at a custom S3-compatible endpoint.
//
// THIS IS THE USUAL FAILURE POINT with Polygon/Massive flat files. Three things
// must all be set or the SDK silently talks to real AWS / 403s:
//  1. BaseEndpoint -> the custom host (the modern v2 way; replaces the old
//     deprecated EndpointResolverWithOptions).
//  2. UsePathStyle = true -> object key is /bucket/key on the host, NOT
//     bucket.host virtual-host style (Polygon/Massive do path style).
//  3. Static credentials + a region (SigV4 needs a region string even though
//     the server ignores it).
func newS3Client(keyID, secret, endpoint string) *s3.Client {
	return s3.New(s3.Options{
		Region:       defaultRegion,
		BaseEndpoint: aws.String(endpoint),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(keyID, secret, ""),
	})
}

func probeAndAnalyze(ctx context.Context, c *s3.Client, bucket, key string) error {
	// 1) HEAD the object first: cheapest way to separate auth vs not-found vs
	//    wrong-endpoint, and to get the on-disk (compressed) size.
	head, err := c.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		// If HEAD 404s, the object key/date might be off. Do a LIST of the month
		// prefix so the operator can see what dates actually exist.
		if isNotFound(err) {
			listMonthPrefix(ctx, c, bucket, key)
		}
		return err
	}
	objSize := aws.ToInt64(head.ContentLength)
	fmt.Printf("    HEAD ok: object exists, compressed size = %d bytes (%.1f MB)\n",
		objSize, float64(objSize)/(1024*1024))

	// 2) GET + gunzip + stream the CSV.
	obj, err := c.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return err
	}
	defer obj.Body.Close()

	gz, err := gzip.NewReader(obj.Body)
	if err != nil {
		return fmt.Errorf("gunzip failed (got the object but it is not valid gzip; "+
			"endpoint may have returned an error/HTML body): %w", err)
	}
	defer gz.Close()

	return analyzeCSV(gz, objSize)
}

func analyzeCSV(r io.Reader, objSize int64) error {
	cr := csv.NewReader(r)
	cr.ReuseRecord = true
	cr.FieldsPerRecord = -1 // tolerate header vs data length mismatch

	// Expected header / column order:
	//   ticker,volume,open,close,high,low,window_start,transactions
	const (
		colTicker = 0
		colVolume = 1
		colOpen   = 2
		colClose  = 3
		colHigh   = 4
		colLow    = 5
		colWindow = 6
		colTxns   = 7
	)

	et, err := time.LoadLocation("America/New_York")
	if err != nil {
		return fmt.Errorf("load America/New_York tz (Windows needs Go's embedded "+
			"tzdata; this binary imports it): %w", err)
	}

	var (
		rows        int64
		preMarket   int64 // window_start time-of-day < 09:30 ET (includes 04:00-09:30)
		regular     int64 // 09:30 <= t < 16:00 ET
		afterHours  int64 // 16:00 <= t < 20:00 ET
		distinct    = map[string]struct{}{}
		samples     [][]string
		earliestET  time.Time
		latestET    time.Time
		sawHeader   bool
		headerLooks bool
		parseErrs   int64
	)

	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			parseErrs++
			if parseErrs > 50 {
				return fmt.Errorf("too many CSV parse errors (>50); aborting: %w", err)
			}
			continue
		}
		if len(rec) < 8 {
			continue
		}
		// First line is a header.
		if !sawHeader {
			sawHeader = true
			headerLooks = strings.EqualFold(rec[colTicker], "ticker") &&
				strings.EqualFold(rec[colWindow], "window_start")
			fmt.Printf("    header   : %s\n", strings.Join(rec, ","))
			if !headerLooks {
				fmt.Println("    WARNING: header not as expected; treating line 1 as data")
			} else {
				continue
			}
		}

		rows++
		distinct[rec[colTicker]] = struct{}{}

		// window_start is Unix NANOSECONDS UTC.
		if ns, err := strconv.ParseInt(rec[colWindow], 10, 64); err == nil {
			tUTC := time.Unix(0, ns).UTC()
			tET := tUTC.In(et)
			if earliestET.IsZero() || tET.Before(earliestET) {
				earliestET = tET
			}
			if tET.After(latestET) {
				latestET = tET
			}
			switch bucketOfDay(tET) {
			case sessPre:
				preMarket++
			case sessReg:
				regular++
			case sessAfter:
				afterHours++
			}
		} else {
			parseErrs++
		}

		// Keep the first 5 data rows, rendered with ET time, as samples.
		if len(samples) < 5 {
			cp := make([]string, len(rec))
			copy(cp, rec)
			samples = append(samples, cp)
		}
	}

	// ---- Report ----
	fmt.Println()
	fmt.Printf("    rows (data) : %d\n", rows)
	fmt.Printf("    distinct tickers : %d\n", len(distinct))
	fmt.Printf("    parse errors : %d\n", parseErrs)
	if !earliestET.IsZero() {
		fmt.Printf("    time span ET : %s  ->  %s\n",
			earliestET.Format("2006-01-02 15:04 MST"),
			latestET.Format("2006-01-02 15:04 MST"))
	}
	fmt.Printf("    pre-market rows (<09:30 ET, incl 04:00) : %d\n", preMarket)
	fmt.Printf("    regular-hours rows (09:30-16:00 ET)     : %d\n", regular)
	fmt.Printf("    after-hours rows (16:00-20:00 ET)       : %d\n", afterHours)

	fmt.Println("\n    sample rows (window_start rendered as ET):")
	for _, s := range samples {
		etStr := "?"
		if ns, err := strconv.ParseInt(s[6], 10, 64); err == nil {
			etLoc, _ := time.LoadLocation("America/New_York")
			etStr = time.Unix(0, ns).In(etLoc).Format("15:04:05 MST")
		}
		fmt.Printf("      %-6s vol=%-9s o=%-8s c=%-8s h=%-8s l=%-8s win=%s(%s) txns=%s\n",
			s[0], s[1], s[2], s[3], s[4], s[5], s[6], etStr, s[7])
	}

	// ---- Verdicts ----
	fmt.Println()
	if rows == 0 {
		return errors.New("file parsed but contained ZERO data rows")
	}
	if !headerLooks {
		fmt.Println("    NOTE: header did not match expected column names; verify schema")
	}
	if preMarket > 0 {
		fmt.Printf("    PASS: extended-hours (pre-09:30 ET) data present -> 04:00 anchor is feasible\n")
	} else {
		fmt.Printf("    FAIL: NO pre-09:30 ET rows found -> this file may be regular-hours only; " +
			"the 04:00 RVOL anchor could not be built from it\n")
		return errors.New("no pre-market rows; extended-hours coverage not confirmed")
	}
	_ = objSize
	return nil
}

type sess int

const (
	sessPre sess = iota
	sessReg
	sessAfter
	sessOther
)

// bucketOfDay classifies an ET timestamp by US equity session window.
func bucketOfDay(tET time.Time) sess {
	mins := tET.Hour()*60 + tET.Minute()
	const (
		open0930 = 9*60 + 30
		close16  = 16 * 60
		end20    = 20 * 60
		start04  = 4 * 60
	)
	switch {
	case mins >= start04 && mins < open0930:
		return sessPre
	case mins >= open0930 && mins < close16:
		return sessReg
	case mins >= close16 && mins < end20:
		return sessAfter
	default:
		return sessOther
	}
}

// listMonthPrefix lists the YYYY/MM/ prefix so the operator can see which dates
// actually have files (helps when a HEAD 404s due to a wrong/holiday date).
func listMonthPrefix(ctx context.Context, c *s3.Client, bucket, key string) {
	// key = us_stocks_sip/minute_aggs_v1/2026/06/2026-06-22.csv.gz
	idx := strings.LastIndex(key, "/")
	if idx < 0 {
		return
	}
	prefix := key[:idx+1]
	fmt.Printf("    object not found; listing prefix %q to show available dates:\n", prefix)
	p := s3.NewListObjectsV2Paginator(c, &s3.ListObjectsV2Input{
		Bucket:  aws.String(bucket),
		Prefix:  aws.String(prefix),
		MaxKeys: aws.Int32(100),
	})
	var keys []string
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			fmt.Printf("      (list failed: %v)\n", err)
			return
		}
		for _, o := range page.Contents {
			keys = append(keys, aws.ToString(o.Key))
		}
	}
	if len(keys) == 0 {
		fmt.Println("      (prefix is empty too -- check the path layout / entitlement)")
		return
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("      %s\n", k)
	}
}

// classify turns raw SDK errors into operator-friendly diagnoses, distinguishing
// the three failure modes that look similar over a custom endpoint.
func classify(err error, endpoint string) error {
	if err == nil {
		return nil
	}
	if isNotFound(err) {
		return fmt.Errorf("OBJECT NOT FOUND on %s: the bucket is reachable and auth "+
			"likely worked, but this date's file does not exist (wrong date / "+
			"holiday / not yet published / wrong path). See the prefix listing above. "+
			"underlying: %w", endpoint, err)
	}
	if isAuthError(err) {
		return fmt.Errorf("AUTH/CREDENTIALS REJECTED on %s: the S3 Access Key ID / "+
			"Secret are wrong, or this account's plan does not include flat-files "+
			"S3 access. Confirm you used the S3 keys from the dashboard (NOT the "+
			"REST API key). underlying: %w", endpoint, err)
	}
	var oe interface{ Error() string }
	if errors.As(err, &oe) {
		s := strings.ToLower(oe.Error())
		if strings.Contains(s, "no such host") ||
			strings.Contains(s, "dial tcp") ||
			strings.Contains(s, "connection refused") ||
			strings.Contains(s, "timeout") ||
			strings.Contains(s, "certificate") {
			return fmt.Errorf("ENDPOINT/NETWORK PROBLEM on %s: host did not resolve / "+
				"connect / TLS failed. The flat-files host may be different; try "+
				"setting MASSIVE_S3_ENDPOINT explicitly. underlying: %w", endpoint, err)
		}
	}
	return fmt.Errorf("UNCLASSIFIED error on %s: %w", endpoint, err)
}

func isNotFound(err error) bool {
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}
	var nf *types.NotFound
	if errors.As(err, &nf) {
		return true
	}
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return true
		}
	}
	return false
}

func isAuthError(err error) bool {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch",
			"403", "Forbidden", "AllAccessDisabled", "InvalidToken":
			return true
		}
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "access denied") ||
		strings.Contains(s, "403") ||
		strings.Contains(s, "signaturedoesnotmatch") ||
		strings.Contains(s, "invalidaccesskeyid")
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}
