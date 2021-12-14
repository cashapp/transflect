package main

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"text/template"

	"github.com/alecthomas/kong"
	"github.com/cashapp/transflect/pkg/transflect"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

var version = "v0.0.0"

type config struct {
	Address   string `arg:"" help:"gRPC reflection server address, host:port" optional:"" group:"gRPC source:"`
	Plaintext bool   `short:"p" help:"Use plain-text; no TLS" group:"gRPC source:"`
	Authority string `help:"Authoritative server name; used as HTTP/2 ':authority' pseudo-header" group:"gRPC source:"`

	Out    string `arg:"" help:"Output file. Default: Stdout" default:"-" group:"Output:"`
	Format string `short:"f" help:"output protoset as one of json, base64, none, envoy, istio, protoset" enum:"json,base64,none,envoy,istio,protoset" default:"json" group:"Output:"`

	HTTPPort uint32 `help:"Port to serve HTTP/JSON" default:"9999"  group:"Envoy:"`

	App            string `help:"App used in Istio EnvoyFilter generation" group:"Istio:"`
	Namespace      string `help:"Namespace used in Istio EnvoyFilter generation" group:"Istio:"`
	IstioPort      uint32 `help:"Port to match in Istio EnvoyFilter generation" default:"8080"  group:"Istio:"`
	HTTPPathPrefix string `help:"Default path prefix for grpc Methods that are not annotated; e.g. /api"`

	LogFormat string           `help:"Log format ('json', 'std')" enum:"json,std" default:"std" group:"Other"`
	Version   kong.VersionFlag `short:"V" help:"Print version information" group:"Other"`
}

type tmplData struct {
	ProtosetBase64 string
	Services       []string

	AppPort  int
	HTTPPort uint32

	App          string
	Namespace    string
	IstioAppPort uint32
}

func main() {
	cfg := &config{}
	_ = kong.Parse(cfg, kong.Vars{"version": version})
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(cfg *config) error {
	var protoset *descriptorpb.FileDescriptorSet
	var services []string
	var err error
	if protoset, services, err = protosetFromReflection(cfg); err != nil {
		return err
	}
	return writeOutput(cfg, protoset, services)
}

func protosetFromReflection(cfg *config) (*descriptorpb.FileDescriptorSet, []string, error) {
	var opts []grpc.DialOption
	if cfg.Plaintext {
		opts = append(opts, grpc.WithInsecure())
	}
	if cfg.Authority != "" {
		opts = append(opts, grpc.WithAuthority(cfg.Authority))
	}

	ctx := context.Background()
	addr := cfg.Address
	fds, services, err := transflect.GetFileDescriptorSet(ctx, addr, cfg.HTTPPathPrefix, opts...)
	if err != nil {
		return nil, nil, errors.Wrap(err, "cannot create file descriptor set")
	}
	return fds, services, nil
}

func writeOutput(cfg *config, protoset *descriptorpb.FileDescriptorSet, services []string) error {
	out := os.Stdout
	if cfg.Out != "-" {
		var err error
		if out, err = os.Create(cfg.Out); err != nil {
			return errors.Wrap(err, "cannot create output file")
		}
	}
	switch cfg.Format {
	case "base64":
		return printBase64(out, protoset)
	case "json":
		return printJSON(out, protoset)
	case "envoy":
		return printEnvoy(out, protoset, services, cfg)
	case "istio":
		return printIstio(out, protoset, services, cfg)
	case "protoset":
		return writeProtosetFile(out, protoset)
	}
	return nil
}

func writeProtosetFile(out io.Writer, protoset *descriptorpb.FileDescriptorSet) error {
	b, err := proto.Marshal(protoset)
	if err != nil {
		return errors.Wrap(err, "failed to marshal file descriptor set")
	}
	if _, err := out.Write(b); err != nil {
		return errors.Wrap(err, "failed to write file descriptor set")
	}
	return nil
}

func printJSON(w io.Writer, m proto.Message) error {
	out, err := protojson.Marshal(m)
	if err != nil {
		return errors.Wrap(err, "cannot JSON marshal proto message")
	}
	fmt.Fprintln(w, string(out))
	return nil
}

func printBase64(w io.Writer, m proto.Message) error {
	str, err := transflect.Base64Proto(m)
	if err != nil {
		return errors.Wrap(err, "cannot base64 encode proto message")
	}
	fmt.Fprintln(w, str)
	return nil
}

//go:embed envoy.yaml.tmpl
var envoyTmpl string

func printEnvoy(w io.Writer, descriptorSet proto.Message, services []string, cfg *config) error {
	protosetBase64, err := transflect.Base64Proto(descriptorSet)
	if err != nil {
		return errors.Wrap(err, "cannot base64 encode FileDescriptorSet")
	}
	port, err := getPort(cfg.Address)
	if err != nil {
		return err
	}

	tmplData := tmplData{
		ProtosetBase64: protosetBase64,
		Services:       services,
		AppPort:        port,
		HTTPPort:       cfg.HTTPPort,
	}
	t := template.Must(template.New("envoy").Parse(envoyTmpl))
	if err := t.Execute(w, tmplData); err != nil {
		return errors.Wrap(err, "cannot parser envoy template")
	}
	return nil
}

//go:embed istio.yaml.tmpl
var istioTmpl string

func printIstio(w io.Writer, descriptorSet proto.Message, services []string, cfg *config) error {
	protosetBase64, err := transflect.Base64Proto(descriptorSet)
	if err != nil {
		return errors.Wrap(err, "cannot base64 encode FileDescriptorSet")
	}

	tmplData := tmplData{
		ProtosetBase64: protosetBase64,
		Services:       services,
		App:            cfg.App,
		Namespace:      cfg.Namespace,
		IstioAppPort:   cfg.IstioPort,
	}
	t := template.Must(template.New("istio").Parse(istioTmpl))
	if err := t.Execute(w, tmplData); err != nil {
		return errors.Wrap(err, "cannot parser istio template")
	}
	return nil
}

func getPort(addr string) (int, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, errors.Wrap(err, "cannot parse address")
	}
	i, err := strconv.Atoi(port)
	if err != nil {
		return 0, errors.Wrap(err, "cannot parse port")
	}
	return i, nil
}
