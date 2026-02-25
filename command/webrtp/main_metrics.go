package main

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/adaptor"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	otextport "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

var (
	streamClientsGauge     metric.Int64Gauge
	streamBitrateKbpsGauge metric.Float64Gauge
	streamFramerateGauge   metric.Float64Gauge
)

func MetricsInit(serviceName, endpoint string) error {
	// Always create Prometheus exporter for /metrics endpoint
	promExporter, err := otextport.New(
		otextport.WithRegisterer(prometheus.DefaultRegisterer),
	)
	if err != nil {
		return err
	}

	// Build options for meter provider
	res, err := resource.New(context.Background(),
		resource.WithAttributes(
			attribute.String("service.name", serviceName),
		),
	)
	if err != nil {
		return err
	}

	opts := []sdkmetric.Option{
		sdkmetric.WithReader(promExporter),
		sdkmetric.WithResource(res),
	}

	// If endpoint is provided, add GRPC push exporter
	if endpoint != "" {
		grpcExporter, err := otlpmetricgrpc.New(context.Background(),
			otlpmetricgrpc.WithEndpoint(endpoint),
			otlpmetricgrpc.WithInsecure(),
		)
		if err != nil {
			return err
		}

		// Create a periodic reader for push export
		periodicReader := sdkmetric.NewPeriodicReader(grpcExporter,
			sdkmetric.WithInterval(10*time.Second),
		)
		opts = append(opts, sdkmetric.WithReader(periodicReader))
	}

	provider := sdkmetric.NewMeterProvider(opts...)
	otel.SetMeterProvider(provider)

	otelMeter := otel.Meter(serviceName)

	streamClientsGauge, err = otelMeter.Int64Gauge("streamer_clients",
		metric.WithDescription("Current number of clients connected to the stream"),
	)
	if err != nil {
		return err
	}

	streamBitrateKbpsGauge, err = otelMeter.Float64Gauge("streamer_bitrate_kbps",
		metric.WithDescription("Current bitrate in Kbps"),
	)
	if err != nil {
		return err
	}

	streamFramerateGauge, err = otelMeter.Float64Gauge("streamer_framerate",
		metric.WithDescription("Current framerate"),
	)
	if err != nil {
		return err
	}

	return nil
}

func MetricsRoute(app *fiber.App) {
	handler := promhttp.Handler()
	app.Get("/metrics", adaptor.HTTPHandler(handler))
}

func MetricsUpdate(streams []*Stream) {
	if streamClientsGauge == nil {
		return
	}

	ctx := context.Background()

	for _, s := range streams {
		stats := s.Hub.GetStats(s.Name)

		nameAttr := attribute.String("name", s.Name)

		// Clients always shows current value (don't reset to 0 when not ready)
		streamClientsGauge.Record(ctx, int64(stats.ClientCount), metric.WithAttributes(nameAttr))

		// Bitrate and framerate show 0 if stream not ready
		if stats.Ready {
			streamBitrateKbpsGauge.Record(ctx, stats.Bitrate, metric.WithAttributes(nameAttr))
			streamFramerateGauge.Record(ctx, stats.Framerate, metric.WithAttributes(nameAttr))
		} else {
			streamBitrateKbpsGauge.Record(ctx, 0, metric.WithAttributes(nameAttr))
			streamFramerateGauge.Record(ctx, 0, metric.WithAttributes(nameAttr))
		}
	}
}
