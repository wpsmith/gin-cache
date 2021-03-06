package cache

import (
	"bytes"
	"crypto/md5"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

const KEY_PREFIX = "gin:cache:"

var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
)

type Cached struct {
	Status   int
	Body     []byte
	Header   http.Header
	ExpireAt time.Time
}

type Store interface {
	Get(string) ([]byte, error)
	Set(string, []byte) error
	Remove(string) error
	Update(string, []byte) error
	Keys() []string
}

type Options struct {
	Store         Store
	Expire        time.Duration
	Headers       []string
	DoNotUseAbort bool
}

func (o *Options) init() {
	if o.Headers == nil {
		o.Headers = []string{
			"User-Agent",
			"Accept",
			"Accept-Encoding",
			"Accept-Language",
			"Cookie",
			"User-Agent",
		}
	}
}

type Cache struct {
	Store
	options Options
	expires map[string]time.Time
}

func (c *Cache) Get(key string) (*Cached, error) {
	if data, err := c.Store.Get(key); err == nil {
		var cch *Cached
		dec := gob.NewDecoder(bytes.NewBuffer(data))
		dec.Decode(&cch)

		if cch.ExpireAt.UnixNano() != 0 && cch.ExpireAt.Before(time.Now()) {
			c.Store.Remove(key)
			return nil, nil
		}

		return cch, nil
	} else {
		return nil, err
	}

	return nil, ErrNotFound
}

func (c *Cache) Set(key string, cch *Cached) error {
	var b bytes.Buffer
	enc := gob.NewEncoder(&b)

	panicIf(enc.Encode(*cch))
	return c.Store.Set(key, b.Bytes())
}

func (c *Cache) Update(key string, cch *Cached) error {
	var b bytes.Buffer
	enc := gob.NewEncoder(&b)

	panicIf(enc.Encode(*cch))

	return c.Store.Update(key, b.Bytes())
}

type wrappedWriter struct {
	gin.ResponseWriter
	body bytes.Buffer
}

func (rw *wrappedWriter) Write(body []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(body)
	if err == nil {
		rw.body.Write(body)
	}
	return n, err
}

func New(o ...Options) gin.HandlerFunc {
	opts := Options{
		Store:  NewInMemory(),
		Expire: 0,
	}

	for _, i := range o {
		opts = i
		break
	}
	opts.init()

	cache := Cache{
		Store:   opts.Store,
		options: opts,
		expires: make(map[string]time.Time),
	}

	return func(c *gin.Context) {

		// only GET method available for caching
		if c.Request.Method != "GET" {
			c.Next()
			return
		}

		tohash := c.Request.URL.RequestURI()
		for _, k := range cache.options.Headers {
			if v, ok := c.Request.Header[k]; ok {
				tohash += k
				tohash += strings.Join(v, "")
			}
		}

		key := KEY_PREFIX + md5String(tohash)

		// Get time from gin.Context
		tn, _ := c.Get("TimeNow")
		timeNow := tn.(time.Time)

		if cch, _ := cache.Get(key); cch == nil {
			// cache miss
			writer := c.Writer
			rw := wrappedWriter{ResponseWriter: c.Writer}
			c.Writer = &rw
			c.Writer.Header().Add("Etag", key)
			c.Writer.Header().Add("X-Gin-Cache-Hit", "MISS")
			c.Writer.Header().Add("Cache-Control", getCacheControl(getTimeDiff(timeNow, cache.options.Expire).Nanoseconds() / 1e9))
			c.Next()
			c.Writer = writer

			cache.Set(key, &Cached{
				Status: rw.Status(),
				Body:   rw.body.Bytes(),
				Header: http.Header(rw.Header()),
				ExpireAt: func() time.Time {
					if cache.options.Expire == 0 {
						return time.Time{}
					} else {
						return getExpiresAtTime(timeNow, cache.options.Expire)
					}
				}(),
			})

		} else {
			// cache found
			//start := time.Now()
			c.Writer.WriteHeader(cch.Status)
			for k, val := range cch.Header {
				for _, v := range val {
					c.Writer.Header().Add(k, v)
				}
			}
			c.Writer.Header().Set("X-Gin-Cache-Hit", "HIT")
			c.Writer.Header().Set("Cache-Control", getCacheControl(getTimeDiff(timeNow, cache.options.Expire).Nanoseconds() / 1e9))
			//c.Writer.Header().Set("Cache-Control", getCacheControl(getTimeDiffFromNow(cache.options.Expire).Nanoseconds() / 1e9))

			//t := fmt.Sprintf("%f ms", timeNow.Sub(start).Seconds()*1000)
			//c.Writer.Header().Add("X-Gin-Cache", t)

			c.Writer.Write(cch.Body)
			if !cache.options.DoNotUseAbort {
				c.Abort()
			}
		}
	}
}


func getCacheControl(maxAge int64) string {
	if maxAge == 0 {
		return "max-age=0, no-cache, no-store, must-revalidate"
	}

	return "max-age=" + strconv.FormatInt(maxAge, 10) + ", public"
}

func getTimeDiff(t time.Time, defaultTime time.Duration) time.Duration {
	futureTime := roundTimeUp(t, defaultTime)
	return futureTime.Sub(t)
}

func getExpiresAtTime(t time.Time, defaultTime time.Duration) time.Time {
	return roundTimeUp(t, defaultTime)
}

func roundTimeUp(t time.Time, defaultTime time.Duration) time.Time {
	ti := t.Truncate(defaultTime)
	ti = ti.Add(defaultTime)
	return ti
}


func md5String(url string) string {
	h := md5.New()
	io.WriteString(h, url)
	return hex.EncodeToString(h.Sum(nil))
}

func init() {
	gob.Register(Cached{})
}

func panicIf(err error) {
	if err != nil {
		panic(err)
	}
}
