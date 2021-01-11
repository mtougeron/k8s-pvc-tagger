// Licensed to Michael Tougeron <github@e.tougeron.com> under
// one or more contributor license agreements. See the LICENSE
// file distributed with this work for additional information
// regarding copyright ownership.
// Michael Tougeron <github@e.tougeron.com> licenses this file
// to you under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package main

import (
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/client"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ec2/ec2iface"
	"github.com/prometheus/client_golang/prometheus"
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

func (client *Client) addVolumeTags(volumeID string, tags map[string]string) {
	var ec2Tags []*ec2.Tag
	for k, v := range tags {
		ec2Tags = append(ec2Tags, &ec2.Tag{Key: aws.String(k), Value: aws.String(v)})
	}

	// Add tags to the volume
	_, err := client.CreateTags(&ec2.CreateTagsInput{
		Resources: []*string{aws.String(volumeID)},
		Tags:      ec2Tags,
	})
	if err != nil {
		log.Errorln("Could not create tags for volumeID:", volumeID, err)
		promActionsTotal.With(prometheus.Labels{"status": "error"}).Inc()
		return
	} else {
		promActionsTotal.With(prometheus.Labels{"status": "success"}).Inc()
	}
}

func (client *Client) deleteVolumeTags(volumeID string, tags []string) {
	var ec2Tags []*ec2.Tag
	for _, k := range tags {
		ec2Tags = append(ec2Tags, &ec2.Tag{Key: aws.String(k)})
	}

	// Add tags to the volume
	_, err := client.DeleteTags(&ec2.DeleteTagsInput{
		Resources: []*string{aws.String(volumeID)},
		Tags:      ec2Tags,
	})
	if err != nil {
		log.Errorln("Could not delete tags for volumeID:", volumeID, err)
		promActionsTotal.With(prometheus.Labels{"status": "error"}).Inc()
		return
	} else {
		promActionsTotal.With(prometheus.Labels{"status": "success"}).Inc()
	}
}
