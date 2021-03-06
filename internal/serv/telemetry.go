package serv

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"

	"contrib.go.opencensus.io/exporter/aws"
	"contrib.go.opencensus.io/exporter/prometheus"
	"contrib.go.opencensus.io/exporter/stackdriver"

	"contrib.go.opencensus.io/exporter/zipkin"
	"contrib.go.opencensus.io/integrations/ocsql"
	stdzipkin "github.com/openzipkin/zipkin-go"
	httpreporter "github.com/openzipkin/zipkin-go/reporter/http"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/trace"
	"go.opencensus.io/zpages"
)

func enableObservability(mux *http.ServeMux) (func(), error) {
	// Enable OpenCensus zPages
	if conf.Telemetry.Debug {
		zpages.Handle(mux, "/telemetry")
	}

	// Enable ocsql metrics with OpenCensus
	ocsql.RegisterAllViews()

	var mex view.Exporter
	var tex trace.Exporter

	var mCloseFn, tCloseFn func()
	var err error

	// Set up the metrics exporter
	switch conf.Telemetry.Metrics.Exporter {
	case "prometheus":
		ep := "/metrics"

		if conf.Telemetry.Metrics.Endpoint != "" {
			ep = conf.Telemetry.Metrics.Endpoint
		}

		ex, err1 := prometheus.NewExporter(prometheus.Options{Namespace: conf.Telemetry.Metrics.Namespace})
		if err == nil {
			mux.Handle(ep, ex)
			log.Printf("INF Prometheus exporter listening on: %s", ep)
		}
		mex, err = view.Exporter(ex), err1

	case "stackdriver":
		mex, err = stackdriver.NewExporter(stackdriver.Options{ProjectID: conf.Telemetry.Metrics.Key})
		if err == nil {
			log.Println("INF Google Stackdriver exporter initialized")
		}

	case "":
		log.Println("WRN OpenCensus: no metrics exporter defined")

	default:
		err = fmt.Errorf("invalid metrics exporter")
	}

	if err != nil {
		return nil, fmt.Errorf("ERR OpenCensus: %s: %v", conf.Telemetry.Metrics, err)
	}

	if mex != nil {
		// Register the exporter
		view.RegisterExporter(mex)
	}

	// Set up the tracing exporter
	switch conf.Telemetry.Tracing.Exporter {
	case "xray", "aws":
		ex, err1 := aws.NewExporter(aws.WithVersion("latest"))
		if err == nil {
			tCloseFn = func() { ex.Flush() }
			log.Println("INF Amazon X-Ray exporter initialized")
		}
		tex, err = trace.Exporter(ex), err1

	case "zipkin":
		// The local endpoint stores the name and address of the local service
		lep, err := stdzipkin.NewEndpoint(conf.AppName, conf.hostPort)
		if err != nil {
			return nil, err
		}

		// The Zipkin reporter takes collected spans from the app and reports them to the backend
		// http://localhost:9411/api/v2/spans is the default for the Zipkin Span v2
		re := httpreporter.NewReporter(conf.Telemetry.Tracing.Endpoint)
		tCloseFn = func() { re.Close() }
		tex = zipkin.NewExporter(re, lep)

	case "":
		log.Println("WRN OpenCensus: no traceing exporter defined")

	default:
		err = fmt.Errorf("invalid tracing exporter")
	}

	if err != nil {
		return nil, fmt.Errorf("ERR OpenCensus: %s: %v",
			conf.Telemetry.Tracing.Exporter,
			err)
	}

	if tex != nil {
		trace.RegisterExporter(tex)
		sample := conf.Telemetry.Tracing.Sample

		if sample == "always" {
			trace.ApplyConfig(trace.Config{DefaultSampler: trace.AlwaysSample()})

		} else {
			prob := 0.5
			if v, err := strconv.ParseFloat(sample, 10); err == nil {
				prob = v
			}
			trace.ApplyConfig(trace.Config{DefaultSampler: trace.ProbabilitySampler(prob)})

		}
	}

	var closeOnce sync.Once

	return func() {
		// Flush and shutdown the Zipkin HTTP reporter
		closeOnce.Do(func() {
			if mCloseFn != nil {
				mCloseFn()
			}
			if tCloseFn != nil {
				tCloseFn()
			}
		})
	}, err
}
