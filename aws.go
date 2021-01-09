package main

import (
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/client"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ec2/ec2iface"
	log "github.com/sirupsen/logrus"
)

var (
	// awsSession the AWS Session
	awsSession *session.Session
	ec2Client  *Client
)

const (
	// Matching strings for region
	regexpAWSRegion = `^[\w]{2}[-][\w]{4,9}[-][\d]$`

	// Default AWS region.
	defaultAWSRegion = "us-east-1"
)

// Client EC2 client interface
type Client struct {
	ec2iface.EC2API
}

// CustomRetryer for custom retry settings
type CustomRetryer struct {
	client.DefaultRetryer
}

func createAWSSession(awsRegion string) *session.Session {
	// Build an AWS session
	log.Debugln("Building AWS session")
	awsConfig := aws.NewConfig().WithCredentialsChainVerboseErrors(true)
	awsConfig.Region = aws.String(awsRegion)
	minDelay, _ := time.ParseDuration("1s")
	maxDelay, _ := time.ParseDuration("10s")
	awsConfig.Retryer = CustomRetryer{DefaultRetryer: client.DefaultRetryer{
		NumMaxRetries:    5,
		MinRetryDelay:    minDelay,
		MaxRetryDelay:    maxDelay,
		MinThrottleDelay: minDelay,
		MaxThrottleDelay: maxDelay,
	}}

	return session.Must(session.NewSession(awsConfig))
}

// newEC2Client initializes an EC2 client
func newEC2Client() (*Client, error) {
	svc := ec2.New(awsSession)
	return &Client{svc}, nil
}

func (client *Client) tagVolume(volumeID string, tags map[string]string) {
	log.Infoln("volumeID:", volumeID)
	log.Infoln("tags:", tags)

	var ec2Tags []*ec2.Tag
	for k, v := range tags {
		ec2Tags = append(ec2Tags, &ec2.Tag{Key: aws.String(k), Value: aws.String(v)})
	}

	// Add tags to the created instance
	_, err := client.CreateTags(&ec2.CreateTagsInput{
		Resources: []*string{aws.String(volumeID)},
		Tags:      ec2Tags,
	})
	if err != nil {
		log.Println("Could not create tags for volumeID", volumeID, err)
		return
	}
}
