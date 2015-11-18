package amazoncf

/*
 * This Driver will utilize a cloud formation stack to create an instance, a lot of the configuration,
 * security group, instance type, etc will be delegated to the cloud formation template.
 *
Todo
 * Pass additional Paramaters to the CloudFormation
 * Handle sititation where stack creation fails,  currently the driver just hangs waiting for completion
**/

import (
	"crypto/md5"
	"crypto/rand"
	"fmt"
	"io"

	"github.com/docker/machine/libmachine/drivers"
	"github.com/docker/machine/libmachine/log"
	"github.com/docker/machine/libmachine/mcnflag"
	"github.com/docker/machine/libmachine/mcnutils"
	"github.com/docker/machine/libmachine/state"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/aws/aws-sdk-go/service/ec2"
)

var (
	dockerPort = 2376
	swarmPort  = 3376
)

const (
	defaultSSHUser = "ubuntu"
)

const driverName = "amazoncf"

type Driver struct {
	*drivers.BaseDriver
	Id                string
	CloudFormationURL string
	SSHPrivateKeyPath string
	InstanceId        string
	PrivateIPAddress  string
	KeyPairName       string
	UsePrivateIP      bool
}

func NewDriver(hostName, storePath string) *Driver {
	id := generateId()
	return &Driver{
		Id: id,
		BaseDriver: &drivers.BaseDriver{
			MachineName: hostName,
			StorePath:   storePath,
			SSHUser:     defaultSSHUser,
		},
	}
}

func (d *Driver) GetCreateFlags() []mcnflag.Flag {
	return []mcnflag.Flag{
		mcnflag.StringFlag{
			Name:  "cloudformation-url",
			Usage: "S3 URL of the CloudFormation File",
		},
		mcnflag.StringFlag{
			Name:  "cloudformation-keypairname",
			Usage: "SSH KeyPair to use",
		},
		mcnflag.StringFlag{
			Name:  "cloudformation-keypath",
			Usage: "keypath to SSH Private Key",
		},
		mcnflag.StringFlag{
			Name:  "cloudformation-ssh-user",
			Usage: "set the name of the ssh user",
			Value: defaultSSHUser,
		},
		mcnflag.BoolFlag{
			Name:  "cloudformation-use-private-address",
			Usage: "Force the usage of private IP address",
		},
	}
}

func (d *Driver) SetConfigFromFlags(flags drivers.DriverOptions) error {
	d.CloudFormationURL = flags.String("cloudformation-url")
	d.SSHPrivateKeyPath = flags.String("cloudformation-keypath")
	d.KeyPairName = flags.String("cloudformation-keypairname")
	d.SSHUser = flags.String("cloudformation-ssh-user")
	d.UsePrivateIP = flags.Bool("cloudformation-use-private-address")

	if d.CloudFormationURL == "" {
		return fmt.Errorf("cloudformation driver requires the --cloudformation-url")
	}

	if d.SSHPrivateKeyPath == "" {
		return fmt.Errorf("cloudformation driver requires the --cloudformation-keypath")
	}

	if d.KeyPairName == "" {
		return fmt.Errorf("cloudformation driver requires the --cloudformation-keypairname")
	}

	return nil
}

func (d *Driver) DriverName() string {
	return driverName
}

func (d *Driver) PreCreateCheck() error {

	//no precreate checks at the moment

	return nil
}

func (d *Driver) Create() error {

	log.Debugf("Creating a new Instance for Stack: %s", d.MachineName)

	if err := mcnutils.CopyFile(d.SSHPrivateKeyPath, d.GetSSHKeyPath()); err != nil {
		return err
	}

	svc := cloudformation.New(session.New())

	params := &cloudformation.CreateStackInput{
		StackName:   aws.String(d.MachineName),
		TemplateURL: aws.String(d.CloudFormationURL),
		Parameters: []*cloudformation.Parameter{
			{
				ParameterKey:   aws.String("KeyName"),
				ParameterValue: aws.String(d.KeyPairName),
			},
		},
	}
	_, err := svc.CreateStack(params)

	if err != nil {
		return err
	}

	if err := mcnutils.WaitFor(d.stackAvailable); err != nil {
		return err
	}

	if err := d.getInstanceInfo(); err != nil {
		return err
	}

	log.Debugf("created instance ID %s, IP address %s, Private IP address %s",
		d.InstanceId,
		d.IPAddress,
		d.PrivateIPAddress,
	)

	return nil
}

func (d *Driver) stackAvailable() bool {

	log.Debug("Checking if the stack is available ")

	svc := cloudformation.New(session.New())

	params := &cloudformation.DescribeStacksInput{
		StackName: aws.String(d.MachineName),
	}
	resp, err := svc.DescribeStacks(params)

	if err != nil {
		log.Infof("Houston we have a problem")
		log.Infof(err.Error())
		return false
	}
	if *resp.Stacks[0].StackStatus == cloudformation.ResourceStatusCreateComplete {
		return true
	} else {
		log.Debug("Stack Not Available Yet")
		return false
	}
}

func (d *Driver) getInstanceInfo() error {

	svc := cloudformation.New(session.New())

	params := &cloudformation.DescribeStacksInput{
		StackName: aws.String(d.MachineName),
	}
	resp, err := svc.DescribeStacks(params)

	if err != nil {
		return err
	}

	for _, element := range resp.Stacks[0].Outputs {
		outputV := *element.OutputValue
		if *element.OutputKey == "PrivateIp" {
			d.PrivateIPAddress = outputV
		}
		if *element.OutputKey == "InstanceID" {
			d.InstanceId = outputV
		}
		if *element.OutputKey == "IpAddress" {
			d.IPAddress = outputV
		}

	}

	return nil
}

func (d *Driver) GetURL() (string, error) {

	ip, err := d.GetIP()
	if err != nil {
		return "", err
	}
	if ip == "" {
		return "", nil
	}
	return fmt.Sprintf("tcp://%s:%d", ip, dockerPort), nil
}

func (d *Driver) GetIP() (string, error) {

	log.Debugf("the ip is %s ", *d.getInstance().PrivateIpAddress)

	instance := d.getInstance()

	if d.UsePrivateIP {
		return *instance.PrivateIpAddress, nil
	}

	return *instance.PublicIpAddress, nil
}

func (d *Driver) getInstance() ec2.Instance {
	svc := ec2.New(session.New())

	params := &ec2.DescribeInstancesInput{

		InstanceIds: []*string{
			aws.String(d.InstanceId), // Required
			// More values...
		},
	}

	resp, err := svc.DescribeInstances(params)

	if err != nil {

		log.Debug(err.Error())

	}

	//this should return error
	return *resp.Reservations[0].Instances[0]

}

func (d *Driver) GetState() (state.State, error) {

	//TODOO use EC2 instance info to get IP
	//handle error
	inst := d.getInstance()

	switch *inst.State.Name {
	case "pending":
		return state.Starting, nil
	case "running":
		return state.Running, nil
	case "stopping":
		return state.Stopping, nil
	case "shutting-down":
		return state.Stopping, nil
	case "stopped":
		return state.Stopped, nil
	default:
		return state.Error, nil
	}
}

func (d *Driver) GetSSHHostname() (string, error) {
	return d.GetIP()
}

func (d *Driver) GetSSHUsername() string {

	if d.SSHUser == "" {
		d.SSHUser = "ubuntu"
	}
	return d.SSHUser
}

func (d *Driver) Start() error {

	svc := ec2.New(session.New())

	params := &ec2.StartInstancesInput{
		InstanceIds: []*string{ // Required
			aws.String(d.InstanceId), // Required
			// More values...
		},
	}
	_, err := svc.StartInstances(params)

	if err != nil {
		return err
	}

	if err := d.waitForInstance(); err != nil {
		return err
	}

	return nil
}

func (d *Driver) instanceIsRunning() bool {

	st, err := d.GetState()
	if err != nil {
		log.Debug(err)
	}
	if st == state.Running {
		return true
	}
	return false
}

func (d *Driver) waitForInstance() error {

	if err := mcnutils.WaitFor(d.instanceIsRunning); err != nil {
		return err
	}

	return nil
}

func (d *Driver) Restart() error {

	svc := ec2.New(session.New())

	params := &ec2.RebootInstancesInput{
		InstanceIds: []*string{ // Required
			aws.String(d.InstanceId), // Required
			// More values...
		},
	}
	_, err := svc.RebootInstances(params)

	if err != nil {

		return err
	}

	if err := d.waitForInstance(); err != nil {
		return err
	}

	return nil
}

func (d *Driver) Kill() error {

	svc := ec2.New(session.New())

	params := &ec2.StopInstancesInput{
		InstanceIds: []*string{ // Required
			aws.String(d.InstanceId), // Required
			// More values...
		},
	}
	_, err := svc.StopInstances(params)

	if err != nil {

		return err
	}

	if err := d.waitForInstance(); err != nil {
		return err
	}

	return nil
}

func (d *Driver) Stop() error {

	svc := ec2.New(session.New())

	params := &ec2.StopInstancesInput{
		InstanceIds: []*string{ // Required
			aws.String(d.InstanceId), // Required
			// More values...
		},
	}
	_, err := svc.StopInstances(params)

	if err != nil {
		return err
	}

	if err := d.waitForInstance(); err != nil {
		return err
	}

	return nil
}

func (d *Driver) Remove() error {

	svc := cloudformation.New(session.New())

	params := &cloudformation.DeleteStackInput{
		StackName: aws.String(d.MachineName), // Required
	}
	_, err := svc.DeleteStack(params)

	if err != nil {
		return err
	}

	return nil
}

func generateId() string {
	rb := make([]byte, 10)
	_, err := rand.Read(rb)
	if err != nil {
		log.Warnf("Unable to generate id: %s", err)
	}

	h := md5.New()
	io.WriteString(h, string(rb))
	return fmt.Sprintf("%x", h.Sum(nil))
}