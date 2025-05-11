package thumbnailgenerator

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

func GeneratePdfThumbnail(pdfPath string, outputDir string, outputImagePrefix string) (string, error) {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create output directory '%s': %w", outputDir, err)
	}
	outputImagePath := filepath.Join(outputDir, outputImagePrefix+".jpg")
	cmd := exec.Command("pdftoppm",
		"-jpeg",
		"-r", "72",
		"-f", "1",
		"-l", "1",
		"-singlefile",
		pdfPath,
		filepath.Join(outputDir, outputImagePrefix),
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	log.Printf("Executing command: %s\n", cmd.String())

	err := cmd.Run()

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("pdftoppm failed with exit code %d: %s\nStderr: %s\nStdout: %s",
				exitErr.ExitCode(), exitErr.Error(), stderr.String(), stdout.String())
		}
		return "", fmt.Errorf("failed to run pdftoppm: %w\nStderr: %s\nStdout: %s",
			err, stderr.String(), stdout.String())
	}

	log.Printf("pdftoppm executed successfully.\nStdout: %s\nStderr: %s\n", stdout.String(), stderr.String())

	if _, statErr := os.Stat(outputImagePath); os.IsNotExist(statErr) {
		return "", fmt.Errorf("output thumbnail file '%s' was not created by pdftoppm", outputImagePath)
	}

	return outputImagePath, nil
}

func ConvertToWebp(jpegPath, webpPath string) error {
  log.Println("converting ", jpegPath, " to ", webpPath)
	cmd := exec.Command("cwebp",
		"-q", "80",
		jpegPath,
		"-o", webpPath,
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	log.Printf("Executing command: %s\n", cmd.String())

	err := cmd.Run()

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("cwebp failed with exit code %d: %s\nStderr: %s\nStdout: %s",
				exitErr.ExitCode(), exitErr.Error(), stderr.String(), stdout.String())
		}
		return fmt.Errorf("failed to run cwepb: %w\nStderr: %s\nStdout: %s",
			err, stderr.String(), stdout.String())
	}

	log.Printf("cwebp executed successfully.\nStdout: %s\nStderr: %s\n", stdout.String(), stderr.String())

	if _, statErr := os.Stat(webpPath); os.IsNotExist(statErr) {
		return fmt.Errorf("output thumbnail file '%s' was not created by cwebp", webpPath)
	}
	return nil
}
