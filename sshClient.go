package goScp

import (
	"bytes"
	"fmt"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"io"
	"io/ioutil"
	"log"
	"net"
	"os"
	"strings"
	"sync"
)

const (
	// VERSION represents the current software version of goScp
	VERSION = "0.0.2"
)

func getAgent() (agent.Agent, error) {
	agentConn, err := net.Dial("unix", os.Getenv("SSH_AUTH_SOCK"))
	return agent.NewClient(agentConn), err
}

func withAgentSSHConfig(username string) (*ssh.ClientConfig, error) {
	agent, err := getAgent()
	if err != nil {
		return &ssh.ClientConfig{}, err
	}
	config := &ssh.ClientConfig{
		User: username,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeysCallback(agent.Signers),
		},
	}
	return config, nil
}

func withoutAgentSSHConfig(username string, sshKeyFile SSHKeyfile) (*ssh.ClientConfig, error) {
	keyFilePath := fmt.Sprintf("%s/%s", sshKeyFile.Path, sshKeyFile.Filename)
	keyFileContents, err := ioutil.ReadFile(keyFilePath)
	if err != nil {
		return &ssh.ClientConfig{}, err
	}
	signer, err := ssh.ParsePrivateKey(keyFileContents)
	if err != nil {
		return &ssh.ClientConfig{}, err
	}

	config := &ssh.ClientConfig{
		User: username,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
	}

	return config, nil
}

// Connect creates an SSH Client connection to the remote host
func Connect(sshKeyFile SSHKeyfile, sshCredentials SSHCredentials, remoteMachine RemoteHost, usingSSHAgent bool) (*ssh.Client, error) {
	// An SSH client is represented with a ClientConn.
	//
	// To authenticate with the remote server you must pass at least one
	// implementation of AuthMethod via the Auth field in ClientConfig.
	var config *ssh.ClientConfig
	var err error
	if usingSSHAgent {
		config, err = withAgentSSHConfig(sshCredentials.Username)
	} else {
		config, err = withoutAgentSSHConfig(sshCredentials.Username, sshKeyFile)
	}

	client, err := ssh.Dial("tcp", remoteMachine.Host+":"+remoteMachine.Port, config)

	return client, err
}

func ExecuteCommand(client *ssh.Client, cmd string) (string, error) {
	// Each ClientConn can support multiple interactive sessions,
	// represented by a Session.
	session, err := client.NewSession()
	if err != nil {
		log.Fatal("Failed to create session: " + err.Error())
	}
	defer session.Close()

	// Once a Session is created, you can execute a single command on
	// the remote side using the Run method.
	var b bytes.Buffer
	session.Stdout = &b
	if err := session.Run(cmd); err != nil {
		return "", err
	}

	return b.String(), nil
}

func CopyRemoteFileToLocal(client *ssh.Client, remoteFilePath string, remoteFilename string, localFilePath string, localFileName string) error {
	// Each ClientConn can support multiple interactive sessions,
	// represented by a Session.
	session, err := client.NewSession()
	if err != nil {
		log.Fatal("Failed to create session: " + err.Error())
	}
	defer session.Close()

	writer, err := session.StdinPipe()
	if err != nil {
		return err
	}

	reader, err := session.StdoutPipe()
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	wg.Add(1)

	go func(writer io.WriteCloser, reader io.Reader, wg *sync.WaitGroup) {
		successfulByte := []byte{0}

		// Send a null byte saying that we are ready to receive the data
		writer.Write(successfulByte)
		// We want to first receive the command input from remote machine
		// e.g. C0644 113828 test.csv
		scpCommandArray := make([]byte, 100)
		bytesRead, err := reader.Read(scpCommandArray)
		if err != nil {
			if err == io.EOF {
				//no problem.
			} else {
				log.Fatalf("Error reading standard input: %s", err.Error())
			}
		}

		scpStartLine := string(scpCommandArray[:bytesRead])
		scpStartLineArray := strings.Split(scpStartLine, " ")

		filePermission := scpStartLineArray[0][1:]
		fileSize := scpStartLineArray[1]
		fileName := scpStartLineArray[2]

		log.Printf("File with permissions: %s, File Size: %s, File Name: %s", filePermission, fileSize, fileName)

		// Confirm to the remote host that we have received the command line
		writer.Write(successfulByte)
		// Now we want to start receiving the file itself from the remote machine
		fileContents := make([]byte, 1)
		var file *os.File
		if localFileName == "" {
			file = createNewFile(localFilePath + "/" + fileName)
		} else {
			file = createNewFile(localFilePath + "/" + localFileName)
		}
		more := true
		for more {
			bytesRead, err = reader.Read(fileContents)
			if err != nil {
				if err == io.EOF {
					more = false
				} else {
					log.Fatalf("Error reading standard input: %s", err.Error())
				}
			}
			writeParitalToFile(file, fileContents[:bytesRead])
			writer.Write(successfulByte)
		}
		err = file.Sync()
		if err != nil {
			log.Fatal(err)
		}
		wg.Done()
	}(writer, reader, &wg)

	session.Run("/usr/bin/scp -f " + remoteFilePath + "/" + remoteFilename)
	wg.Wait()
	writer.Close()
	return nil
}

func CopyLocalFileToRemote(client *ssh.Client, localFilePath string, filename string) error {
	// Each ClientConn can support multiple interactive sessions,
	// represented by a Session.
	session, err := client.NewSession()
	if err != nil {
		log.Fatal("Failed to create session: " + err.Error())
	}
	defer session.Close()

	writer, err := session.StdinPipe()
	if err != nil {
		return err
	}
	defer writer.Close()

	go func() {
		fileContents, _ := ioutil.ReadFile(localFilePath + "/" + filename)
		content := string(fileContents)
		fmt.Fprintln(writer, "C0644", len(content), filename)
		fmt.Fprint(writer, content)
		fmt.Fprintln(writer, "\x00") // transfer end with \x00\
	}()

	session.Run("/usr/bin/scp -t ./")
	return nil
}
