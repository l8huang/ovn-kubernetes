package healthcheck

import (
	grpc "google.golang.org/grpc"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
)

type HealthCheckResponse_ServingStatus = grpc_health_v1.HealthCheckResponse_ServingStatus

const (
	HealthCheckResponse_UNKNOWN         = grpc_health_v1.HealthCheckResponse_UNKNOWN
	HealthCheckResponse_SERVING         = grpc_health_v1.HealthCheckResponse_SERVING
	HealthCheckResponse_NOT_SERVING     = grpc_health_v1.HealthCheckResponse_NOT_SERVING
	HealthCheckResponse_SERVICE_UNKNOWN = grpc_health_v1.HealthCheckResponse_SERVICE_UNKNOWN
)

type HealthCheckRequest = grpc_health_v1.HealthCheckRequest
type HealthCheckResponse = grpc_health_v1.HealthCheckResponse

type HealthClient = grpc_health_v1.HealthClient
type Health_WatchClient = grpc_health_v1.Health_WatchClient

type HealthServer = grpc_health_v1.HealthServer
type Health_WatchServer = grpc_health_v1.Health_WatchServer
type UnimplementedHealthServer = grpc_health_v1.UnimplementedHealthServer
type UnsafeHealthServer = grpc_health_v1.UnsafeHealthServer

var Health_ServiceDesc = grpc_health_v1.Health_ServiceDesc

func NewHealthClient(cc grpc.ClientConnInterface) HealthClient {
	return grpc_health_v1.NewHealthClient(cc)
}

func RegisterHealthServer(s grpc.ServiceRegistrar, srv HealthServer) {
	grpc_health_v1.RegisterHealthServer(s, srv)
}
