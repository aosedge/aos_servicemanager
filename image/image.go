package image

import (
	"crypto/sha256"
	"crypto/sha512"
	"errors"
	"io"
	"os"
	"reflect"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// FileInfo file info
type FileInfo struct {
	Sha256 []byte
	Sha512 []byte
	Size   uint64
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// CheckFileInfo checks if file matches FileInfo
func CheckFileInfo(fileName string, fileInfo FileInfo) (err error) {
	file, err := os.Open(fileName)
	if err != nil {
		return err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return err
	}

	if uint64(stat.Size()) != fileInfo.Size {
		return errors.New("file size mistmatch")
	}

	hash256 := sha256.New()

	if _, err := io.Copy(hash256, file); err != nil {
		return err
	}

	if !reflect.DeepEqual(hash256.Sum(nil), fileInfo.Sha256) {
		return errors.New("checksum sha256 mistmatch")
	}

	hash512 := sha512.New()

	if _, err = file.Seek(0, 0); err != nil {
		return err
	}

	if _, err := io.Copy(hash512, file); err != nil {
		return err
	}

	if !reflect.DeepEqual(hash512.Sum(nil), fileInfo.Sha512) {
		return errors.New("checksum sha512 mistmatch")
	}

	return nil
}

// CreateFileInfo creates FileInfo from existing file
func CreateFileInfo(fileName string) (fileInfo FileInfo, err error) {
	file, err := os.Open(fileName)
	if err != nil {
		return fileInfo, err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return fileInfo, err
	}

	fileInfo.Size = uint64(stat.Size())

	hash256 := sha256.New()

	if _, err := io.Copy(hash256, file); err != nil {
		return fileInfo, err
	}

	fileInfo.Sha256 = hash256.Sum(nil)

	hash512 := sha512.New()

	if _, err = file.Seek(0, 0); err != nil {
		return fileInfo, err
	}

	if _, err := io.Copy(hash512, file); err != nil {
		return fileInfo, err
	}

	fileInfo.Sha512 = hash512.Sum(nil)

	return fileInfo, nil
}
