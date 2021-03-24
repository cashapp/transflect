package transflect

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/juliaogris/guppy/pkg/echo"
	"github.com/juliaogris/guppy/pkg/rguide"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

func TestTransflectSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TestTransflectSuite in short mode due to slow setup.")
	}
	suite.Run(t, &TransflectSuite{})
}

type TransflectSuite struct {
	suite.Suite
	server *grpc.Server
	addr   string
}

func (s *TransflectSuite) SetupSuite() {
	t := s.T()
	s.server = grpc.NewServer()
	echoServer := &echo.Server{}
	echo.RegisterEchoServer(s.server, echoServer)
	rguideServer := rguide.NewServer()
	rguide.RegisterRouteGuideServer(s.server, rguideServer)
	reflection.Register(s.server)
	lis, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	port := lis.Addr().(*net.TCPAddr).Port
	s.addr = fmt.Sprintf("localhost:%d", port)
	serve := func() {
		if err := s.server.Serve(lis); err != nil {
			panic(err)
		}
	}
	go serve()
}

func (s *TransflectSuite) TearDownSuite() {
	s.server.Stop()
}

func (s *TransflectSuite) TestProtoset() {
	t := s.T()
	ctx := context.Background()
	fds, services, err := GetFileDescriptorSet(ctx, s.addr, grpc.WithInsecure())
	require.NoError(t, err)
	got := make([]string, len(fds.File))
	for i, file := range fds.File {
		got[i] = file.GetName()
	}
	want := []string{
		"google/api/http.proto",
		"google/protobuf/descriptor.proto",
		"google/api/annotations.proto",
		"echo/echo.proto",
		"reflection/grpc_reflection_v1alpha/reflection.proto",
		"rguide/routeguide.proto",
	}
	require.Equal(t, want, got)
	wantServices := []string{
		"echo.Echo",
		"grpc.reflection.v1alpha.ServerReflection",
		"rguide.RouteGuide",
	}
	require.Equal(t, wantServices, services)
}
