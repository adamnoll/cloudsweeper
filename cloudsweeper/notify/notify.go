// Copyright (c) 2018 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: BSD-2-Clause

// Package notify is responsible for all actions related
// to notifying employees and managers about their resources.
//
// Email credentials must be set using os environment variables
// in order to be able to send mail. Note that monthToDateAddressee
// is intended to be sent weekly to your entire org. The
// totalSumAddressee is meant to send a total report to the person
// in your org monitoring costs.
//
// The templates.go file contains all email templates used for
// notifications. This uses the native Go template engine.
package notify

import (
	"fmt"
	"log"
	"time"

	"github.com/cloudtools/cloudsweeper/mailer"

	"github.com/cloudtools/cloudsweeper/cloud"
	"github.com/cloudtools/cloudsweeper/cloud/billing"
	"github.com/cloudtools/cloudsweeper/cloud/filter"
	cs "github.com/cloudtools/cloudsweeper/cloudsweeper"
)

// Client is used to perform the notify actions. It must be
// initalized with correct values to work properly.
type Client struct {
	config *Config
}

// Config is a configuration for the notify Client
type Config struct {
	SMTPUsername           string
	SMTPPassword           string
	SMTPServer             string
	SMTPPort               int
	DisplayName            string
	MailFrom               string
	EmailDomain            string
	BillingReportAddressee string
	TotalSumAddresse       string
}

// Init will initialize a notify Client with a given Config
func Init(config *Config) *Client {
	return &Client{config: config}
}

type resourceMailData struct {
	Owner          string
	OwnerID        string
	Instances      []cloud.Instance
	Images         []cloud.Image
	Snapshots      []cloud.Snapshot
	Volumes        []cloud.Volume
	Buckets        []cloud.Bucket
	HoursInAdvance int
}

func (d *resourceMailData) ResourceCount() int {
	return len(d.Images) + len(d.Instances) + len(d.Snapshots) + len(d.Volumes) + len(d.Buckets)
}

func (d *resourceMailData) SendEmail(client mailer.Client, domain, mailTemplate, title string, debugAddressees ...string) {
	mailContent, err := generateMail(d, mailTemplate)
	if err != nil {
		log.Fatalln("Could not generate email:", err)
	}

	ownerMail := fmt.Sprintf("%s@%s", d.Owner, domain)
	recieverMail := convertEmailExceptions(ownerMail)
	log.Printf("Sending out email to %s\n", recieverMail)
	addressees := append(debugAddressees, recieverMail)
	err = client.SendEmail(title, mailContent, addressees...)
	if err != nil {
		log.Fatalf("Failed to email %s: %s\n", recieverMail, err)
	}
}

type monthToDateData struct {
	CSP              cloud.CSP
	TotalCost        float64
	SortedUsers      billing.UserList
	MinimumTotalCost float64
	MinimumCost      float64
	AccountToUser    map[string]string
}

func initTotalSummaryMailData(totalSumAddressee string) *resourceMailData {
	return &resourceMailData{
		Owner:     totalSumAddressee,
		Instances: []cloud.Instance{},
		Images:    []cloud.Image{},
		Snapshots: []cloud.Snapshot{},
		Volumes:   []cloud.Volume{},
		Buckets:   []cloud.Bucket{},
	}
}

func initManagerToMailDataMapping(managers cs.Employees) map[string]*resourceMailData {
	result := make(map[string]*resourceMailData)
	for _, manager := range managers {
		result[manager.Username] = &resourceMailData{
			Owner:     manager.Username,
			Instances: []cloud.Instance{},
			Images:    []cloud.Image{},
			Snapshots: []cloud.Snapshot{},
			Volumes:   []cloud.Volume{},
			Buckets:   []cloud.Bucket{},
		}
	}
	return result
}

// OldResourceReview will review (but not do any cleanup action) old resources
// that an owner might want to consider doing something about. The owner is then
// sent an email with a list of these resources. Resources are sent for review
// if they fulfil any of the following rules:
//		- Resource is older than 30 days
//		- A whitelisted resource is older than 6 months
//		- An instance marked with do-not-delete is older than a week
func (c *Client) OldResourceReview(mngr cloud.ResourceManager, org *cs.Organization, csp cloud.CSP) {
	allCompute := mngr.AllResourcesPerAccount()
	allBuckets := mngr.BucketsPerAccount()
	accountUserMapping := org.AccountToUserMapping(csp)
	userEmployeeMapping := org.UsernameToEmployeeMapping()
	totalSummaryMailData := initTotalSummaryMailData(c.config.TotalSumAddresse)
	managerToMailDataMapping := initManagerToMailDataMapping(org.Managers)

	// Create filters
	generalFilter := filter.New()
	generalFilter.AddGeneralRule(filter.OlderThanXDays(30))

	whitelistFilter := filter.New()
	whitelistFilter.OverrideWhitelist = true
	whitelistFilter.AddGeneralRule(filter.OlderThanXMonths(6))

	// These only apply to instances
	dndFilter := filter.New()
	dndFilter.AddGeneralRule(filter.HasTag("no-not-delete"))
	dndFilter.AddGeneralRule(filter.OlderThanXDays(7))

	dndFilter2 := filter.New()
	dndFilter2.AddGeneralRule(filter.NameContains("do-not-delete"))
	dndFilter2.AddGeneralRule(filter.OlderThanXDays(7))

	for account, resources := range allCompute {
		log.Println("Performing old resource review in", account)
		username := accountUserMapping[account]
		employee := userEmployeeMapping[username]

		// Apply filters
		userMailData := resourceMailData{
			Owner:     username,
			Instances: filter.Instances(resources.Instances, generalFilter, whitelistFilter, dndFilter, dndFilter2),
			Images:    filter.Images(resources.Images, generalFilter, whitelistFilter),
			Volumes:   filter.Volumes(resources.Volumes, generalFilter, whitelistFilter),
			Snapshots: filter.Snapshots(resources.Snapshots, generalFilter, whitelistFilter),
			Buckets:   []cloud.Bucket{},
		}
		if buckets, ok := allBuckets[account]; ok {
			userMailData.Buckets = filter.Buckets(buckets, generalFilter, whitelistFilter)
		}

		// Add to the manager summary
		if managerSummaryMailData, ok := managerToMailDataMapping[employee.Manager.Username]; ok { // safe or org _should_ have thrown an error
			managerSummaryMailData.Instances = append(managerSummaryMailData.Instances, userMailData.Instances...)
			managerSummaryMailData.Images = append(managerSummaryMailData.Images, userMailData.Images...)
			managerSummaryMailData.Snapshots = append(managerSummaryMailData.Snapshots, userMailData.Snapshots...)
			managerSummaryMailData.Volumes = append(managerSummaryMailData.Volumes, userMailData.Volumes...)
			managerSummaryMailData.Buckets = append(managerSummaryMailData.Buckets, userMailData.Buckets...)
		} else {
			log.Fatalf("%s is not a manager??? Verify `organization.go` and the org repo itself for issues", employee.Manager.Username)
		}

		// Add to the total summary
		totalSummaryMailData.Instances = append(totalSummaryMailData.Instances, userMailData.Instances...)
		totalSummaryMailData.Images = append(totalSummaryMailData.Images, userMailData.Images...)
		totalSummaryMailData.Snapshots = append(totalSummaryMailData.Snapshots, userMailData.Snapshots...)
		totalSummaryMailData.Volumes = append(totalSummaryMailData.Volumes, userMailData.Volumes...)
		totalSummaryMailData.Buckets = append(totalSummaryMailData.Buckets, userMailData.Buckets...)

		if userMailData.ResourceCount() > 0 {
			title := fmt.Sprintf("You have %d old resources to review (%s)", userMailData.ResourceCount(), time.Now().Format("2006-01-02"))
			userMailData.SendEmail(getMailClient(c), c.config.EmailDomain, reviewMailTemplate, title)
		}
	}

	// Send out manager emails
	for username, managerSummaryMailData := range managerToMailDataMapping {
		log.Printf("Collecting old resources to review for %s's team\n", username)
		if managerSummaryMailData.ResourceCount() > 0 {
			title := fmt.Sprintf("Your team has %d old resources to review (%s)", managerSummaryMailData.ResourceCount(), time.Now().Format("2006-01-02"))
			managerSummaryMailData.SendEmail(getMailClient(c), c.config.EmailDomain, managerReviewMailTemplate, title)
		}
	}

	// Send out a total summary
	log.Println("Collecting old resource review for the org")
	title := fmt.Sprintf("Your org has %d old resources to review (%s)", totalSummaryMailData.ResourceCount(), time.Now().Format("2006-01-02"))
	totalSummaryMailData.SendEmail(getMailClient(c), c.config.EmailDomain, totalReviewMailTemplate, title)
}

// UntaggedResourcesReview will look for resources without any tags, and
// send out a mail encouraging to tag tag them
func (c *Client) UntaggedResourcesReview(mngr cloud.ResourceManager, accountUserMapping map[string]string) {
	// We only care about untagged resources in EC2
	allCompute := mngr.AllResourcesPerAccount()
	for account, resources := range allCompute {
		log.Printf("Performing untagged resources review in %s", account)
		untaggedFilter := filter.New()
		untaggedFilter.AddGeneralRule(filter.Negate(filter.HasTag("Name")))

		// We care about un-tagged whitelisted resources too
		untaggedFilter.OverrideWhitelist = true

		username := accountUserMapping[account]
		mailData := resourceMailData{
			Owner:     username,
			OwnerID:   account,
			Instances: filter.Instances(resources.Instances, untaggedFilter),
			// Only report on instances for now
			//Images:    filter.Images(resources.Images, untaggedFilter),
			//Snapshots: filter.Snapshots(resources.Snapshots, untaggedFilter),
			//Volumes:   filter.Volumes(resources.Volumes, untaggedFilter),
			Buckets: []cloud.Bucket{},
		}

		if mailData.ResourceCount() > 0 {
			// Send mail
			title := fmt.Sprintf("You have %d un-tagged resources to review (%s)", mailData.ResourceCount(), time.Now().Format("2006-01-02"))
			// You can add some debug email address to ensure it works
			// debugAddressees := []string{"ben@example.com"}
			// mailData.SendEmail(getMailClient(c), c.config.EmailDomain, untaggedMailTemplate, title, debugAddressees...)
			mailData.SendEmail(getMailClient(c), c.config.EmailDomain, untaggedMailTemplate, title)
		}
	}
}

// DeletionWarning will find resources which are about to be deleted within
// `hoursInAdvance` hours, and send an email to the owner of those resources
// with a warning. Resources explicitly tagged to be deleted are not included
// in this warning.
func (c *Client) DeletionWarning(hoursInAdvance int, mngr cloud.ResourceManager, accountUserMapping map[string]string) {
	allCompute := mngr.AllResourcesPerAccount()
	allBuckets := mngr.BucketsPerAccount()
	for account, resources := range allCompute {
		ownerName := convertEmailExceptions(accountUserMapping[account])
		fil := filter.New()
		fil.AddGeneralRule(filter.DeleteWithinXHours(hoursInAdvance))
		mailData := resourceMailData{
			ownerName,
			account,
			filter.Instances(resources.Instances, fil),
			filter.Images(resources.Images, fil),
			filter.Snapshots(resources.Snapshots, fil),
			filter.Volumes(resources.Volumes, fil),
			[]cloud.Bucket{},
			hoursInAdvance,
		}
		if buckets, ok := allBuckets[account]; ok {
			mailData.Buckets = filter.Buckets(buckets, fil)
		}

		if mailData.ResourceCount() > 0 {
			// Send email
			title := fmt.Sprintf("Deletion warning, %d resources are cleaned up within %d hours", mailData.ResourceCount(), hoursInAdvance)
			mailData.SendEmail(getMailClient(c), c.config.EmailDomain, deletionWarningTemplate, title)
		}
	}
}

// MonthToDateReport sends an email to engineering with the
// Month-to-Date billing report
func (c *Client) MonthToDateReport(report billing.Report, accountUserMapping map[string]string, sortedByTags bool) {
	mailClient := getMailClient(c)
	var sorted billing.UserList
	if sortedByTags {
		sorted = report.SortedTagsByTotalCost()
	} else {
		sorted = report.SortedUsersByTotalCost()
	}
	reportData := monthToDateData{report.CSP, report.TotalCost(), sorted, billing.MinimumTotalCost, billing.MinimumCost, accountUserMapping}
	mailContent, err := generateMail(reportData, monthToDateTemplate)
	if err != nil {
		log.Fatalln("Could not generate email:", err)
	}
	billingReportMail := fmt.Sprintf("%s@%s", c.config.BillingReportAddressee, c.config.EmailDomain)
	recipientMail := convertEmailExceptions(billingReportMail)
	log.Printf("Sending the Month-to-date report to %s\n", recipientMail)
	title := fmt.Sprintf("Month-to-date %s billing report", report.CSP)
	err = mailClient.SendEmail(title, mailContent, recipientMail)
	if err != nil {
		log.Printf("Failed to email %s: %s\n", recipientMail, err)
	}
}
