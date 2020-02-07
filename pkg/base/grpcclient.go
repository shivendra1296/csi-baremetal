package base

import (
	"net/url"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

//Client encapsulates logic for new gRPC clint
type Client struct {
	GRPCClient *grpc.ClientConn
	Creds      credentials.TransportCredentials
	Endpoint   string
	log        *logrus.Entry
}

//NewClient creates new Client object with hostTCP, port, creds and calls init function
func NewClient(creds credentials.TransportCredentials, endpoint string, logger *logrus.Logger) (*Client, error) {
	client := &Client{
		Creds:    creds,
		Endpoint: endpoint,
	}
	client.SetLogger(logger)
	err := client.initClient()
	if err != nil {
		return nil, err
	}
	return client, nil
}

func (c *Client) SetLogger(logger *logrus.Logger) {
	c.log = logger.WithField("component", "Client")
}

//initClient defines ClientConn field in Client struct
func (c *Client) initClient() error {
	endpoint := c.GetEndpoint()
	var err error
	c.log.Infof("Initialize client for endpoint \"%s\"", endpoint)
	if c.Creds != nil {
		c.GRPCClient, err = grpc.Dial(endpoint, grpc.WithTransportCredentials(c.Creds))
	} else {
		c.GRPCClient, err = grpc.Dial(endpoint, grpc.WithInsecure())
	}
	if err != nil {
		return err
	}
	return nil
}

//Close function calls Close method in ClientConn
func (c *Client) Close() error {
	return c.GRPCClient.Close()
}

// GetEndpoint returns endpoint representation
func (c *Client) GetEndpoint() string {
	u, _ := url.Parse(c.Endpoint)

	if u.Scheme == unix {
		return u.Path
	}

	return u.Host
}
