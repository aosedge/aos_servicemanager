package launcher

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencontainers/runtime-spec/specs-go"
	log "github.com/sirupsen/logrus"
)

func packImage(source, name string) (err error) {
	log.WithFields(log.Fields{"source": source, "name": name}).Debug("Pack image")

	_, err = os.Stat(source)
	if err != nil {
		return err
	}

	writer, err := os.Create(name)
	if err != nil {
		return err
	}

	gzWriter := gzip.NewWriter(writer)
	defer gzWriter.Close()

	tarWriter := tar.NewWriter(gzWriter)
	defer tarWriter.Close()

	return filepath.Walk(source, func(fileName string, fileInfo os.FileInfo, err error) error {
		header, err := tar.FileInfoHeader(fileInfo, fileInfo.Name())
		if err != nil {
			return err
		}

		header.Name = strings.TrimPrefix(strings.Replace(fileName, source, "", -1), string(filepath.Separator))

		if header.Name == "" {
			return nil
		}

		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}

		if !fileInfo.Mode().IsRegular() {
			return nil
		}

		file, err := os.Open(fileName)
		if err != nil {
			return err
		}
		defer file.Close()

		if _, err := io.Copy(tarWriter, file); err != nil {
			return err
		}

		return nil
	})
}

func unpackImage(name, destination string) (err error) {
	log.WithFields(log.Fields{"name": name, "destination": destination}).Debug("Unpack image")

	reader, err := os.Open(name)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(destination, 0755); err != nil {
		return err
	}

	gzReader, err := gzip.NewReader(reader)
	if err != nil {
		return err
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)

	for {
		header, err := tarReader.Next()

		switch {
		case err == io.EOF:
			return nil

		case err != nil:
			return err

		case header == nil:
			continue
		}

		target := filepath.Join(destination, header.Name)

		switch header.Typeflag {
		case tar.TypeDir:
			if _, err := os.Stat(target); err != nil {
				if err := os.MkdirAll(target, 0755); err != nil {
					return err
				}
			}

		case tar.TypeReg:
			dir, _ := filepath.Split(target)
			if _, err := os.Stat(dir); err != nil {
				if err := os.MkdirAll(dir, 0755); err != nil {
					return err
				}
			}

			file, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			defer file.Close()

			if _, err := io.Copy(file, tarReader); err != nil {
				return err
			}
		}
	}
}

func getServiceSpec(configFile string) (spec specs.Spec, err error) {
	raw, err := ioutil.ReadFile(configFile)
	if err != nil {
		return spec, err
	}

	if err = json.Unmarshal(raw, &spec); err != nil {
		return spec, err
	}

	return spec, nil
}

func writeServiceSpec(spec *specs.Spec, configFile string) (err error) {
	f, err := os.Create(configFile)
	if err != nil {
		return err
	}
	defer f.Close()

	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "\t")

	if err := encoder.Encode(spec); err != nil {
		return err
	}

	return nil
}
