package envoy

import (
	"encoding/json"
	"flag"
	"github.com/hashicorp/consul/agent"
	"github.com/hashicorp/consul/agent/xds"
	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/sdk/testutil"
	"github.com/mitchellh/cli"
	"github.com/stretchr/testify/require"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "update golden files")

func TestEnvoyCommand_noTabs(t *testing.T) {
	t.Parallel()
	if strings.ContainsRune(New(nil).Help(), '\t') {
		t.Fatal("help has tabs")
	}
}

func TestEnvoyGateway_Validation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		args   []string
		output string
	}{
		{
			"-register for non-gateway",
			[]string{"-register"},
			"Auto-Registration can only be used for gateways",
		},
		{
			"-mesh-gateway and -gateway cannot be combined",
			[]string{"-register", "-mesh-gateway", "-gateway", "mesh"},
			"The mesh-gateway flag is deprecated and cannot be used alongside the gateway flag",
		},
		{
			"no proxy registration specified nor discovered",
			[]string{""},
			"No proxy ID specified",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ui := cli.NewMockUi()
			c := New(ui)
			c.init()

			code := c.Run(tc.args)
			if code == 0 {
				t.Errorf("%s: expected non-zero exit", tc.name)
			}

			output := ui.ErrorWriter.String()
			if !strings.Contains(output, tc.output) {
				t.Errorf("expected %q to contain %q", output, tc.output)
			}
		})
	}
}

// testSetAndResetEnv sets the env vars passed as KEY=value strings in the
// current ENV and returns a func() that will undo it's work at the end of the
// test for use with defer.
func testSetAndResetEnv(t *testing.T, env []string) func() {
	old := make(map[string]*string)
	for _, e := range env {
		pair := strings.SplitN(e, "=", 2)
		current := os.Getenv(pair[0])
		if current != "" {
			old[pair[0]] = &current
		} else {
			// save it as a nil so we know to remove again
			old[pair[0]] = nil
		}
		os.Setenv(pair[0], pair[1])
	}
	// Return a func that will reset to old values
	return func() {
		for k, v := range old {
			if v == nil {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, *v)
			}
		}
	}
}

// This tests the args we use to generate the template directly because they
// encapsulate all the argument and default handling code which is where most of
// the logic is. We also allow generating golden files but only for cases that
// pass the test of having their template args generated as expected.
func TestGenerateConfig(t *testing.T) {
	cases := []struct {
		Name        string
		Flags       []string
		Env         []string
		Files       map[string]string
		ProxyConfig map[string]interface{}
		GRPCPort    int // only used for testing custom-configured grpc port
		WantArgs    BootstrapTplArgs
		WantErr     string
	}{
		{
			Name:    "no-args",
			Flags:   []string{},
			Env:     []string{},
			WantErr: "No proxy ID specified",
		},
		{
			Name:  "defaults",
			Flags: []string{"-proxy-id", "test-proxy"},
			WantArgs: BootstrapTplArgs{
				EnvoyVersion: defaultEnvoyVersion,
				ProxyCluster: "test-proxy",
				ProxyID:      "test-proxy",
				GRPC: GRPC{
					AgentAddress: "127.0.0.1",
					AgentPort:    "8502", // Note this is the gRPC port
				},
				AdminAccessLogPath:    "/dev/null",
				AdminBindAddress:      "127.0.0.1",
				AdminBindPort:         "19000",
				LocalAgentClusterName: xds.LocalAgentClusterName,
			},
		},
		{
			Name: "token-arg",
			Flags: []string{"-proxy-id", "test-proxy",
				"-token", "c9a52720-bf6c-4aa6-b8bc-66881a5ade95"},
			WantArgs: BootstrapTplArgs{
				EnvoyVersion: defaultEnvoyVersion,
				ProxyCluster: "test-proxy",
				ProxyID:      "test-proxy",
				GRPC: GRPC{
					AgentAddress: "127.0.0.1",
					AgentPort:    "8502", // Note this is the gRPC port
				},
				AdminAccessLogPath:    "/dev/null",
				AdminBindAddress:      "127.0.0.1",
				AdminBindPort:         "19000",
				LocalAgentClusterName: xds.LocalAgentClusterName,
				Token:                 "c9a52720-bf6c-4aa6-b8bc-66881a5ade95",
			},
		},
		{
			Name:  "token-env",
			Flags: []string{"-proxy-id", "test-proxy"},
			Env: []string{
				"CONSUL_HTTP_TOKEN=c9a52720-bf6c-4aa6-b8bc-66881a5ade95",
			},
			WantArgs: BootstrapTplArgs{
				EnvoyVersion: defaultEnvoyVersion,
				ProxyCluster: "test-proxy",
				ProxyID:      "test-proxy",
				GRPC: GRPC{
					AgentAddress: "127.0.0.1",
					AgentPort:    "8502", // Note this is the gRPC port
				},
				AdminAccessLogPath:    "/dev/null",
				AdminBindAddress:      "127.0.0.1",
				AdminBindPort:         "19000",
				LocalAgentClusterName: xds.LocalAgentClusterName,
				Token:                 "c9a52720-bf6c-4aa6-b8bc-66881a5ade95",
			},
		},
		{
			Name: "token-file-arg",
			Flags: []string{"-proxy-id", "test-proxy",
				"-token-file", "@@TEMPDIR@@token.txt",
			},
			Files: map[string]string{
				"token.txt": "c9a52720-bf6c-4aa6-b8bc-66881a5ade95",
			},
			WantArgs: BootstrapTplArgs{
				EnvoyVersion: defaultEnvoyVersion,
				ProxyCluster: "test-proxy",
				ProxyID:      "test-proxy",
				GRPC: GRPC{
					AgentAddress: "127.0.0.1",
					AgentPort:    "8502", // Note this is the gRPC port
				},
				AdminAccessLogPath:    "/dev/null",
				AdminBindAddress:      "127.0.0.1",
				AdminBindPort:         "19000",
				LocalAgentClusterName: xds.LocalAgentClusterName,
				Token:                 "c9a52720-bf6c-4aa6-b8bc-66881a5ade95",
			},
		},
		{
			Name:  "token-file-env",
			Flags: []string{"-proxy-id", "test-proxy"},
			Env: []string{
				"CONSUL_HTTP_TOKEN_FILE=@@TEMPDIR@@token.txt",
			},
			Files: map[string]string{
				"token.txt": "c9a52720-bf6c-4aa6-b8bc-66881a5ade95",
			},
			WantArgs: BootstrapTplArgs{
				EnvoyVersion: defaultEnvoyVersion,
				ProxyCluster: "test-proxy",
				ProxyID:      "test-proxy",
				GRPC: GRPC{
					AgentAddress: "127.0.0.1",
					AgentPort:    "8502", // Note this is the gRPC port
				},
				AdminAccessLogPath:    "/dev/null",
				AdminBindAddress:      "127.0.0.1",
				AdminBindPort:         "19000",
				LocalAgentClusterName: xds.LocalAgentClusterName,
				Token:                 "c9a52720-bf6c-4aa6-b8bc-66881a5ade95",
			},
		},
		{
			Name: "grpc-addr-flag",
			Flags: []string{"-proxy-id", "test-proxy",
				"-grpc-addr", "localhost:9999"},
			WantArgs: BootstrapTplArgs{
				EnvoyVersion: defaultEnvoyVersion,
				ProxyCluster: "test-proxy",
				ProxyID:      "test-proxy",
				// Should resolve IP, note this might not resolve the same way
				// everywhere which might make this test brittle but not sure what else
				// to do.
				GRPC: GRPC{
					AgentAddress: "127.0.0.1",
					AgentPort:    "9999",
				},
				AdminAccessLogPath:    "/dev/null",
				AdminBindAddress:      "127.0.0.1",
				AdminBindPort:         "19000",
				LocalAgentClusterName: xds.LocalAgentClusterName,
			},
		},
		{
			Name:  "grpc-addr-env",
			Flags: []string{"-proxy-id", "test-proxy"},
			Env: []string{
				"CONSUL_GRPC_ADDR=localhost:9999",
			},
			WantArgs: BootstrapTplArgs{
				EnvoyVersion: defaultEnvoyVersion,
				ProxyCluster: "test-proxy",
				ProxyID:      "test-proxy",
				// Should resolve IP, note this might not resolve the same way
				// everywhere which might make this test brittle but not sure what else
				// to do.
				GRPC: GRPC{
					AgentAddress: "127.0.0.1",
					AgentPort:    "9999",
				},
				AdminAccessLogPath:    "/dev/null",
				AdminBindAddress:      "127.0.0.1",
				AdminBindPort:         "19000",
				LocalAgentClusterName: xds.LocalAgentClusterName,
			},
		},
		{
			Name: "grpc-addr-unix",
			Flags: []string{"-proxy-id", "test-proxy",
				"-grpc-addr", "unix:///var/run/consul.sock"},
			WantArgs: BootstrapTplArgs{
				EnvoyVersion: defaultEnvoyVersion,
				ProxyCluster: "test-proxy",
				ProxyID:      "test-proxy",
				GRPC: GRPC{
					AgentSocket: "/var/run/consul.sock",
				},
				AdminAccessLogPath:    "/dev/null",
				AdminBindAddress:      "127.0.0.1",
				AdminBindPort:         "19000",
				LocalAgentClusterName: xds.LocalAgentClusterName,
			},
		},
		{
			Name:     "grpc-addr-config",
			Flags:    []string{"-proxy-id", "test-proxy"},
			GRPCPort: 9999,
			WantArgs: BootstrapTplArgs{
				EnvoyVersion: defaultEnvoyVersion,
				ProxyCluster: "test-proxy",
				ProxyID:      "test-proxy",
				// Should resolve IP, note this might not resolve the same way
				// everywhere which might make this test brittle but not sure what else
				// to do.
				GRPC: GRPC{
					AgentAddress: "127.0.0.1",
					AgentPort:    "9999",
				},
				AdminAccessLogPath:    "/dev/null",
				AdminBindAddress:      "127.0.0.1",
				AdminBindPort:         "19000",
				LocalAgentClusterName: xds.LocalAgentClusterName,
			},
		},
		{
			Name:  "access-log-path",
			Flags: []string{"-proxy-id", "test-proxy", "-admin-access-log-path", "/some/path/access.log"},
			WantArgs: BootstrapTplArgs{
				EnvoyVersion: defaultEnvoyVersion,
				ProxyCluster: "test-proxy",
				ProxyID:      "test-proxy",
				// Should resolve IP, note this might not resolve the same way
				// everywhere which might make this test brittle but not sure what else
				// to do.
				GRPC: GRPC{
					AgentAddress: "127.0.0.1",
					AgentPort:    "8502",
				},
				AdminAccessLogPath:    "/some/path/access.log",
				AdminBindAddress:      "127.0.0.1",
				AdminBindPort:         "19000",
				LocalAgentClusterName: xds.LocalAgentClusterName,
			},
		},
		{
			Name:  "missing-ca-file",
			Flags: []string{"-proxy-id", "test-proxy", "-ca-file", "some/path"},
			WantArgs: BootstrapTplArgs{
				EnvoyVersion: defaultEnvoyVersion,
				ProxyCluster: "test-proxy",
				ProxyID:      "test-proxy",
				// Should resolve IP, note this might not resolve the same way
				// everywhere which might make this test brittle but not sure what else
				// to do.
				GRPC: GRPC{
					AgentAddress: "127.0.0.1",
					AgentPort:    "8502",
				},
			},
			WantErr: "Error loading CA File: open some/path: no such file or directory",
		},
		{
			Name:  "existing-ca-file",
			Flags: []string{"-proxy-id", "test-proxy", "-ca-file", "../../../test/ca/root.cer"},
			Env:   []string{"CONSUL_HTTP_SSL=1"},
			WantArgs: BootstrapTplArgs{
				EnvoyVersion: defaultEnvoyVersion,
				ProxyCluster: "test-proxy",
				ProxyID:      "test-proxy",
				// Should resolve IP, note this might not resolve the same way
				// everywhere which might make this test brittle but not sure what else
				// to do.
				GRPC: GRPC{
					AgentAddress: "127.0.0.1",
					AgentPort:    "8502",
					AgentTLS:     true,
				},
				AgentCAPEM:            `-----BEGIN CERTIFICATE-----\nMIIEtzCCA5+gAwIBAgIJAIewRMI8OnvTMA0GCSqGSIb3DQEBBQUAMIGYMQswCQYD\nVQQGEwJVUzELMAkGA1UECBMCQ0ExFjAUBgNVBAcTDVNhbiBGcmFuY2lzY28xHDAa\nBgNVBAoTE0hhc2hpQ29ycCBUZXN0IENlcnQxDDAKBgNVBAsTA0RldjEWMBQGA1UE\nAxMNdGVzdC5pbnRlcm5hbDEgMB4GCSqGSIb3DQEJARYRdGVzdEBpbnRlcm5hbC5j\nb20wHhcNMTQwNDA3MTkwMTA4WhcNMjQwNDA0MTkwMTA4WjCBmDELMAkGA1UEBhMC\nVVMxCzAJBgNVBAgTAkNBMRYwFAYDVQQHEw1TYW4gRnJhbmNpc2NvMRwwGgYDVQQK\nExNIYXNoaUNvcnAgVGVzdCBDZXJ0MQwwCgYDVQQLEwNEZXYxFjAUBgNVBAMTDXRl\nc3QuaW50ZXJuYWwxIDAeBgkqhkiG9w0BCQEWEXRlc3RAaW50ZXJuYWwuY29tMIIB\nIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAxrs6JK4NpiOItxrpNR/1ppUU\nmH7p2BgLCBZ6eHdclle9J56i68adt8J85zaqphCfz6VDP58DsFx+N50PZyjQaDsU\nd0HejRqfHRMtg2O+UQkv4Z66+Vo+gc6uGuANi2xMtSYDVTAqqzF48OOPQDgYkzcG\nxcFZzTRFFZt2vPnyHj8cHcaFo/NMNVh7C3yTXevRGNm9u2mrbxCEeiHzFC2WUnvg\nU2jQuC7Fhnl33Zd3B6d3mQH6O23ncmwxTcPUJe6xZaIRrDuzwUcyhLj5Z3faag/f\npFIIcHSiHRfoqHLGsGg+3swId/zVJSSDHr7pJUu7Cre+vZa63FqDaooqvnisrQID\nAQABo4IBADCB/TAdBgNVHQ4EFgQUo/nrOfqvbee2VklVKIFlyQEbuJUwgc0GA1Ud\nIwSBxTCBwoAUo/nrOfqvbee2VklVKIFlyQEbuJWhgZ6kgZswgZgxCzAJBgNVBAYT\nAlVTMQswCQYDVQQIEwJDQTEWMBQGA1UEBxMNU2FuIEZyYW5jaXNjbzEcMBoGA1UE\nChMTSGFzaGlDb3JwIFRlc3QgQ2VydDEMMAoGA1UECxMDRGV2MRYwFAYDVQQDEw10\nZXN0LmludGVybmFsMSAwHgYJKoZIhvcNAQkBFhF0ZXN0QGludGVybmFsLmNvbYIJ\nAIewRMI8OnvTMAwGA1UdEwQFMAMBAf8wDQYJKoZIhvcNAQEFBQADggEBADa9fV9h\ngjapBlkNmu64WX0Ufub5dsJrdHS8672P30S7ILB7Mk0W8sL65IezRsZnG898yHf9\n2uzmz5OvNTM9K380g7xFlyobSVq+6yqmmSAlA/ptAcIIZT727P5jig/DB7fzJM3g\njctDlEGOmEe50GQXc25VKpcpjAsNQi5ER5gowQ0v3IXNZs+yU+LvxLHc0rUJ/XSp\nlFCAMOqd5uRoMOejnT51G6krvLNzPaQ3N9jQfNVY4Q0zfs0M+6dRWvqfqB9Vyq8/\nPOLMld+HyAZEBk9zK3ZVIXx6XS4dkDnSNR91njLq7eouf6M7+7s/oMQZZRtAfQ6r\nwlW975rYa1ZqEdA=\n-----END CERTIFICATE-----\n`,
				AdminAccessLogPath:    "/dev/null",
				AdminBindAddress:      "127.0.0.1",
				AdminBindPort:         "19000",
				LocalAgentClusterName: xds.LocalAgentClusterName,
			},
		},
		{
			Name:  "custom-bootstrap",
			Flags: []string{"-proxy-id", "test-proxy"},
			ProxyConfig: map[string]interface{}{
				// Add a completely custom bootstrap template. Never mind if this is
				// invalid envoy config just as long as it works and gets the variables
				// interplated.
				"envoy_bootstrap_json_tpl": `
				{
					"admin": {
						"access_log_path": "/dev/null",
						"address": {
							"socket_address": {
								"address": "{{ .AdminBindAddress }}",
								"port_value": {{ .AdminBindPort }}
							}
						}
					},
					"node": {
						"cluster": "{{ .ProxyCluster }}",
						"id": "{{ .ProxyID }}"
					},
					"custom_field": "foo"
				}`,
			},
			WantArgs: BootstrapTplArgs{
				EnvoyVersion: defaultEnvoyVersion,
				ProxyCluster: "test-proxy",
				ProxyID:      "test-proxy",
				GRPC: GRPC{
					AgentAddress: "127.0.0.1",
					AgentPort:    "8502",
				},
				AdminAccessLogPath:    "/dev/null",
				AdminBindAddress:      "127.0.0.1",
				AdminBindPort:         "19000",
				LocalAgentClusterName: xds.LocalAgentClusterName,
			},
		},
		{
			Name:  "extra_-single",
			Flags: []string{"-proxy-id", "test-proxy"},
			ProxyConfig: map[string]interface{}{
				// Add a custom sections with interpolated variables. These are all
				// invalid config syntax too but we are just testing they have the right
				// effect.
				"envoy_extra_static_clusters_json": `
				{
					"name": "fake_cluster_1"
				}`,
				"envoy_extra_static_listeners_json": `
				{
					"name": "fake_listener_1"
				}`,
				"envoy_extra_stats_sinks_json": `
				{
					"name": "fake_sink_1"
				}`,
			},
			WantArgs: BootstrapTplArgs{
				EnvoyVersion: defaultEnvoyVersion,
				ProxyCluster: "test-proxy",
				ProxyID:      "test-proxy",
				GRPC: GRPC{
					AgentAddress: "127.0.0.1",
					AgentPort:    "8502",
				},
				AdminAccessLogPath:    "/dev/null",
				AdminBindAddress:      "127.0.0.1",
				AdminBindPort:         "19000",
				LocalAgentClusterName: xds.LocalAgentClusterName,
			},
		},
		{
			Name:  "extra_-multiple",
			Flags: []string{"-proxy-id", "test-proxy"},
			ProxyConfig: map[string]interface{}{
				// Add a custom sections with interpolated variables. These are all
				// invalid config syntax too but we are just testing they have the right
				// effect.
				"envoy_extra_static_clusters_json": `
				{
					"name": "fake_cluster_1"
				},
				{
					"name": "fake_cluster_2"
				}`,
				"envoy_extra_static_listeners_json": `
				{
					"name": "fake_listener_1"
				},{
					"name": "fake_listener_2"
				}`,
				"envoy_extra_stats_sinks_json": `
				{
					"name": "fake_sink_1"
				} , { "name": "fake_sink_2" }`,
			},
			WantArgs: BootstrapTplArgs{
				EnvoyVersion: defaultEnvoyVersion,
				ProxyCluster: "test-proxy",
				ProxyID:      "test-proxy",
				GRPC: GRPC{
					AgentAddress: "127.0.0.1",
					AgentPort:    "8502",
				},
				AdminAccessLogPath:    "/dev/null",
				AdminBindAddress:      "127.0.0.1",
				AdminBindPort:         "19000",
				LocalAgentClusterName: xds.LocalAgentClusterName,
			},
		},
		{
			Name:  "stats-config-override",
			Flags: []string{"-proxy-id", "test-proxy"},
			ProxyConfig: map[string]interface{}{
				// Add a custom sections with interpolated variables. These are all
				// invalid config syntax too but we are just testing they have the right
				// effect.
				"envoy_stats_config_json": `
				{
					"name": "fake_config"
				}`,
			},
			WantArgs: BootstrapTplArgs{
				EnvoyVersion: defaultEnvoyVersion,
				ProxyCluster: "test-proxy",
				ProxyID:      "test-proxy",
				GRPC: GRPC{
					AgentAddress: "127.0.0.1",
					AgentPort:    "8502",
				},
				AdminAccessLogPath:    "/dev/null",
				AdminBindAddress:      "127.0.0.1",
				AdminBindPort:         "19000",
				LocalAgentClusterName: xds.LocalAgentClusterName,
			},
		},
		{
			Name:  "zipkin-tracing-config",
			Flags: []string{"-proxy-id", "test-proxy"},
			ProxyConfig: map[string]interface{}{
				// Add a custom sections with interpolated variables. These are all
				// invalid config syntax too but we are just testing they have the right
				// effect.
				"envoy_tracing_json": `{
					"http": {
						"name": "envoy.zipkin",
						"config": {
							"collector_cluster": "zipkin",
							"collector_endpoint": "/api/v1/spans"
						}
					}
				}`,
				// Need to setup the cluster to send that too as well
				"envoy_extra_static_clusters_json": `{
					"name": "zipkin",
					"type": "STRICT_DNS",
					"connect_timeout": "5s",
					"load_assignment": {
						"cluster_name": "zipkin",
						"endpoints": [
							{
								"lb_endpoints": [
									{
										"endpoint": {
											"address": {
												"socket_address": {
													"address": "zipkin.service.consul",
													"port_value": 9411
												}
											}
										}
									}
								]
							}
						]
					}
				}`,
			},
			WantArgs: BootstrapTplArgs{
				EnvoyVersion: defaultEnvoyVersion,
				ProxyCluster: "test-proxy",
				ProxyID:      "test-proxy",
				GRPC: GRPC{
					AgentAddress: "127.0.0.1",
					AgentPort:    "8502",
				},
				AdminAccessLogPath:    "/dev/null",
				AdminBindAddress:      "127.0.0.1",
				AdminBindPort:         "19000",
				LocalAgentClusterName: xds.LocalAgentClusterName,
			},
		},
		{
			Name:  "CONSUL_HTTP_ADDR-with-https-scheme-enables-tls",
			Flags: []string{"-proxy-id", "test-proxy"},
			Env:   []string{"CONSUL_HTTP_ADDR=https://127.0.0.1:8888"},
			WantArgs: BootstrapTplArgs{
				EnvoyVersion: defaultEnvoyVersion,
				ProxyCluster: "test-proxy",
				ProxyID:      "test-proxy",
				// Should resolve IP, note this might not resolve the same way
				// everywhere which might make this test brittle but not sure what else
				// to do.
				GRPC: GRPC{
					AgentAddress: "127.0.0.1",
					AgentPort:    "8502",
					AgentTLS:     true,
				},
				AdminAccessLogPath:    "/dev/null",
				AdminBindAddress:      "127.0.0.1",
				AdminBindPort:         "19000",
				LocalAgentClusterName: xds.LocalAgentClusterName,
			},
		},
	}

	copyAndReplaceAll := func(s []string, old, new string) []string {
		out := make([]string, len(s))
		for i, v := range s {
			out[i] = strings.ReplaceAll(v, old, new)
		}
		return out
	}

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			require := require.New(t)

			testDir := testutil.TempDir(t, "envoytest")
			defer os.RemoveAll(testDir)

			if len(tc.Files) > 0 {
				for fn, fv := range tc.Files {
					fullname := filepath.Join(testDir, fn)
					require.NoError(ioutil.WriteFile(fullname, []byte(fv), 0600))
				}
			}

			// Run a mock agent API that just always returns the proxy config in the
			// test.
			srv := httptest.NewServer(testMockAgent(tc.ProxyConfig, tc.GRPCPort))
			defer srv.Close()
			client, err := api.NewClient(&api.Config{Address: srv.URL})
			require.NoError(err)

			testDirPrefix := testDir + string(filepath.Separator)
			myEnv := copyAndReplaceAll(tc.Env, "@@TEMPDIR@@", testDirPrefix)
			defer testSetAndResetEnv(t, myEnv)()

			ui := cli.NewMockUi()
			c := New(ui)
			// explicitly set the client to one which can connect to the httptest.Server
			c.client = client

			// Run the command
			myFlags := copyAndReplaceAll(tc.Flags, "@@TEMPDIR@@", testDirPrefix)
			args := append([]string{"-bootstrap"}, myFlags...)

			require.NoError(c.flags.Parse(args))
			code := c.run(c.flags.Args())
			if tc.WantErr == "" {
				require.Equal(0, code, ui.ErrorWriter.String())
			} else {
				require.Equal(1, code, ui.ErrorWriter.String())
				require.Contains(ui.ErrorWriter.String(), tc.WantErr)
				return
			}

			// Verify we handled the env and flags right first to get correct template
			// args.
			got, err := c.templateArgs()
			require.NoError(err) // Error cases should have returned above
			require.Equal(&tc.WantArgs, got)

			// Actual template output goes to stdout direct to avoid prefix in UI, so
			// generate it again here to assert on.
			actual, err := c.generateConfig()
			require.NoError(err)

			// If we got the arg handling write, verify output
			golden := filepath.Join("testdata", tc.Name+".golden")
			if *update {
				ioutil.WriteFile(golden, actual, 0644)
			}

			expected, err := ioutil.ReadFile(golden)
			require.NoError(err)
			require.Equal(string(expected), string(actual))
		})
	}
}

// testMockAgent combines testMockAgentProxyConfig and testMockAgentSelf,
// routing /agent/service/... requests to testMockAgentProxyConfig and
// routing /agent/self requests to testMockAgentSelf.
func testMockAgent(agentCfg map[string]interface{}, grpcPort int) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/agent/service") {
			testMockAgentProxyConfig(agentCfg)(w, r)
			return
		}

		if strings.Contains(r.URL.Path, "/agent/self") {
			testMockAgentSelf(grpcPort)(w, r)
			return
		}

		http.NotFound(w, r)
	})
}

func testMockAgentProxyConfig(cfg map[string]interface{}) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Parse the proxy-id from the end of the URL (blindly assuming it's correct
		// format)
		proxyID := strings.TrimPrefix(r.URL.Path, "/v1/agent/service/")
		serviceID := strings.TrimSuffix(proxyID, "-sidecar-proxy")

		svc := api.AgentService{
			Kind:    api.ServiceKindConnectProxy,
			ID:      proxyID,
			Service: proxyID,
			Proxy: &api.AgentServiceConnectProxyConfig{
				DestinationServiceName: serviceID,
				DestinationServiceID:   serviceID,
				Config:                 cfg,
			},
		}

		cfgJSON, err := json.Marshal(svc)
		if err != nil {
			w.WriteHeader(500)
			w.Write([]byte(err.Error()))
			return
		}
		w.Write(cfgJSON)
	})
}

func TestEnvoyCommand_canBindInternal(t *testing.T) {
	t.Parallel()
	type testCheck struct {
		expected bool
		addr     string
	}

	type testCase struct {
		ifAddrs []net.Addr
		checks  map[string]testCheck
	}

	parseIPNets := func(t *testing.T, in ...string) []net.Addr {
		var out []net.Addr
		for _, addr := range in {
			ip := net.ParseIP(addr)
			require.NotNil(t, ip)
			out = append(out, &net.IPNet{IP: ip})
		}
		return out
	}

	parseIPs := func(t *testing.T, in ...string) []net.Addr {
		var out []net.Addr
		for _, addr := range in {
			ip := net.ParseIP(addr)
			require.NotNil(t, ip)
			out = append(out, &net.IPAddr{IP: ip})
		}
		return out
	}

	cases := map[string]testCase{
		"IPNet": {
			parseIPNets(t, "10.3.0.2", "198.18.0.1", "2001:db8:a0b:12f0::1"),
			map[string]testCheck{
				"ipv4": {
					true,
					"10.3.0.2",
				},
				"secondary ipv4": {
					true,
					"198.18.0.1",
				},
				"ipv6": {
					true,
					"2001:db8:a0b:12f0::1",
				},
				"ipv4 not found": {
					false,
					"1.2.3.4",
				},
				"ipv6 not found": {
					false,
					"::ffff:192.168.0.1",
				},
			},
		},
		"IPAddr": {
			parseIPs(t, "10.3.0.2", "198.18.0.1", "2001:db8:a0b:12f0::1"),
			map[string]testCheck{
				"ipv4": {
					true,
					"10.3.0.2",
				},
				"secondary ipv4": {
					true,
					"198.18.0.1",
				},
				"ipv6": {
					true,
					"2001:db8:a0b:12f0::1",
				},
				"ipv4 not found": {
					false,
					"1.2.3.4",
				},
				"ipv6 not found": {
					false,
					"::ffff:192.168.0.1",
				},
			},
		},
	}

	for name, tcase := range cases {
		t.Run(name, func(t *testing.T) {
			for checkName, check := range tcase.checks {
				t.Run(checkName, func(t *testing.T) {
					require.Equal(t, check.expected, canBindInternal(check.addr, tcase.ifAddrs))
				})
			}
		})
	}
}

// testMockAgentSelf returns an empty /v1/agent/self response except GRPC
// port is filled in to match the given wantGRPCPort argument.
func testMockAgentSelf(wantGRPCPort int) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := agent.Self{
			DebugConfig: map[string]interface{}{
				"GRPCPort": wantGRPCPort,
			},
		}

		selfJSON, err := json.Marshal(resp)
		if err != nil {
			w.WriteHeader(500)
			w.Write([]byte(err.Error()))
			return
		}
		w.Write(selfJSON)
	})
}
