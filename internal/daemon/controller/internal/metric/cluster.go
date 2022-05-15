package metric

import (
	"context"
	"strings"

	"github.com/hashicorp/boundary/globals"
	"github.com/hashicorp/boundary/internal/errors"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
)

const (
	labelGRpcCode    = "grpc_code"
	labelGRpcService = "grpc_service"
	labelGRpcMethod  = "grpc_method"
	clusterSubSystem = "controller_cluster"
)

// gRpcRequestLatency collects measurements of how long it takes
// the boundary system to reply to a request to the controller cluster
// from the time that boundary received the request.
var gRpcRequestLatency prometheus.ObserverVec = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{
		Namespace: globals.MetricNamespace,
		Subsystem: clusterSubSystem,
		Name:      "grpc_request_duration_seconds",
		Help:      "Histogram of latencies for gRPC requests.",
		Buckets:   prometheus.DefBuckets,
	},
	[]string{labelGRpcCode, labelGRpcService, labelGRpcMethod},
)

// statusFromError retrieves the *status.Status from the provided error.  It'll
// attempt to unwrap the *status.Error, which is something status.FromError
// does not do.
func statusFromError(err error) *status.Status {
	if s, ok := status.FromError(err); ok {
		return s
	}

	type gRPCStatus interface {
		GRPCStatus() *status.Status
	}
	var unwrappedStatus gRPCStatus
	if ok := errors.As(err, &unwrappedStatus); ok {
		return unwrappedStatus.GRPCStatus()
	}

	return status.New(codes.Unknown, "Unknown Code")
}

func splitMethodName(fullMethodName string) (string, string) {
	fullMethodName = strings.TrimPrefix(fullMethodName, "/") // remove leading slash
	if i := strings.Index(fullMethodName, "/"); i >= 0 {
		return fullMethodName[:i], fullMethodName[i+1:]
	}
	return "unknown", "unknown"
}

type metricMethodNameContextKey struct{}

type statsHandler struct{}

func (sh statsHandler) TagRPC(ctx context.Context, i *stats.RPCTagInfo) context.Context {
	return context.WithValue(ctx, metricMethodNameContextKey{}, i.FullMethodName)
}

func (sh statsHandler) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return ctx
}

func (sh statsHandler) HandleConn(context.Context, stats.ConnStats) {
}

func (sh statsHandler) HandleRPC(ctx context.Context, s stats.RPCStats) {
	switch v := s.(type) {
	case *stats.End:
		// Accept the ok, but ignore it. This code doesn't need to panic
		// and if "fullName" is an empty string splitMethodName will
		// set service and method to "unknown".
		fullName, _ := ctx.Value(metricMethodNameContextKey{}).(string)
		service, method := splitMethodName(fullName)
		l := prometheus.Labels{
			labelGRpcMethod:  method,
			labelGRpcService: service,
			labelGRpcCode:    statusFromError(v.Error).Code().String(),
		}
		gRpcRequestLatency.With(l).Observe(v.EndTime.Sub(v.BeginTime).Seconds())
	}
}

var allCodes = []codes.Code{
	codes.OK, codes.InvalidArgument, codes.PermissionDenied,
	codes.FailedPrecondition,

	// Codes which can be generated by the gRPC framework
	codes.Canceled, codes.Unknown, codes.DeadlineExceeded,
	codes.ResourceExhausted, codes.Unimplemented, codes.Internal,
	codes.Unavailable, codes.Unauthenticated,
}

// InstrumentClusterStatsHandler returns a gRPC stats.Handler which observes
// cluster specific metrics. Use with the cluster gRPC server.
func InstrumentClusterStatsHandler() statsHandler {
	return statsHandler{}
}

// InitializeClusterCollectors registers the cluster metrics to the default
// prometheus register and initializes them to 0 for all possible label
// combinations.
func InitializeClusterCollectors(r prometheus.Registerer, server *grpc.Server) {
	if r == nil {
		return
	}
	r.MustRegister(gRpcRequestLatency)

	for serviceName, info := range server.GetServiceInfo() {
		for _, mInfo := range info.Methods {
			for _, c := range allCodes {
				l := prometheus.Labels{
					labelGRpcMethod:  mInfo.Name,
					labelGRpcService: serviceName,
					labelGRpcCode:    c.String(),
				}
				gRpcRequestLatency.With(l)
			}
		}
	}
}