package image_test

import (
	"aos_common/image"
	"io/ioutil"
	"os"
	"os/exec"
	"testing"

	log "github.com/sirupsen/logrus"
)

/*******************************************************************************
 * Init
 ******************************************************************************/

func init() {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: false,
		TimestampFormat:  "2006-01-02 15:04:05.000",
		FullTimestamp:    true})
	log.SetLevel(log.DebugLevel)
	log.SetOutput(os.Stdout)
}

/*******************************************************************************
 * Main
 ******************************************************************************/

func TestMain(m *testing.M) {
	ret := m.Run()

	os.Exit(ret)
}

func TestUntarGZArchive(t *testing.T) {
	// test destination does not exist
	if err := image.UntarGZArchive("./no_tar", "./no_dest"); err == nil {
		t.Errorf("UntarGZArchive should failed:  destination does not exist")
	}

	//test no source tar,gz file
	if err := os.MkdirAll("tmpFolder", 0755); err != nil {
		log.Fatalf("Error creating tmp dir %s", err)
	}
	if err := os.MkdirAll("tmpFolder/outfolder", 0755); err != nil {
		log.Fatalf("Error creating tmp dir %s", err)
	}
	if err := image.UntarGZArchive("./no_tar", "tmpFolder/outfolder"); err == nil {
		t.Errorf("UntarGZArchive should failed:  no such file or directory")
	}

	//test invalid archive
	if err := ioutil.WriteFile("tmpFolder/testArchive.tar.gz", []byte("This is test file"), 0644); err != nil {
		log.Fatalf("Can't write test file: %s", err)
	}
	if err := image.UntarGZArchive("./tmpFolder/testArchive.tar.gz", ""); err == nil {
		t.Errorf("UntarGZArchive should failed: invalid header")
	}

	//prepare source folder and create archive
	if err := os.MkdirAll("tmpFolder/archive_folder", 0755); err != nil {
		log.Fatalf("Error creating tmp dir %s", err)
	}
	if err := os.MkdirAll("tmpFolder/archive_folder/dir1", 0755); err != nil {
		log.Fatalf("Error creating tmp dir %s", err)
	}
	if err := ioutil.WriteFile("tmpFolder/archive_folder/file.txt", []byte("This is test file"), 0644); err != nil {
		log.Fatalf("Can't write test file: %s", err)
	}
	if err := ioutil.WriteFile("tmpFolder/archive_folder/dir1/file2.txt", []byte("This is test file2"), 0644); err != nil {
		log.Fatalf("Can't write test file: %s", err)
	}
	command := exec.Command("tar", "-czf", "tmpFolder/test_archive.tar.gz", "-C", "tmpFolder/archive_folder", ".")
	if err := command.Run(); err != nil {
		log.Fatalf("Can't run tar: %s", err)
	}
	if err := image.UntarGZArchive("./tmpFolder/test_archive.tar.gz", "./tmpFolder/outfolder"); err != nil {
		log.Error("some issue with untar", err)
	}

	//compare source dir and untarred dir
	command = exec.Command("git", "diff", "--no-index", "./tmpFolder/archive_folder/", "./tmpFolder/outfolder/")
	out, _ := command.Output()
	if string(out) != "" {
		t.Errorf("Untar content not identical")
	}

	if err := os.RemoveAll("tmpFolder"); err != nil {
		log.Fatalf("Error deleting tmp dir: %s", err)
	}
}
