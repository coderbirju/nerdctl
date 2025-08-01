/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package commit

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/identity"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/diff"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/rootfs"
	"github.com/containerd/errdefs"
	"github.com/containerd/log"
	"github.com/containerd/platforms"
	"github.com/containerd/stargz-snapshotter/estargz"
	estargzconvert "github.com/containerd/stargz-snapshotter/nativeconverter/estargz"
	zstdchunkedconvert "github.com/containerd/stargz-snapshotter/nativeconverter/zstdchunked"

	"github.com/containerd/nerdctl/v2/pkg/api/types"
	"github.com/containerd/nerdctl/v2/pkg/clientutil"
	"github.com/containerd/nerdctl/v2/pkg/cmd/image"
	"github.com/containerd/nerdctl/v2/pkg/containerutil"
	"github.com/containerd/nerdctl/v2/pkg/imgutil"
	"github.com/containerd/nerdctl/v2/pkg/labels"
)

type Changes struct {
	CMD, Entrypoint []string
}

type Opts struct {
	Author      string
	Message     string
	Ref         string
	Pause       bool
	Changes     Changes
	Compression types.CompressionType
	Format      types.ImageFormat
	types.EstargzOptions
	types.ZstdChunkedOptions
}

var (
	emptyGZLayer = digest.Digest("sha256:4f4fb700ef54461cfa02571ae0db9a0dc1e0cdb5577484a6d75e68dc38e8acc1")
	emptyDigest  = digest.Digest("")
)

func Commit(ctx context.Context, client *containerd.Client, container containerd.Container, opts *Opts, globalOptions types.GlobalCommandOptions) (digest.Digest, error) {
	// Get labels
	containerLabels, err := container.Labels(ctx)
	if err != nil {
		return emptyDigest, err
	}

	// Get datastore
	dataStore, err := clientutil.DataStore(globalOptions.DataRoot, globalOptions.Address)
	if err != nil {
		return emptyDigest, err
	}

	// Ensure we do have a stateDir label
	stateDir := containerLabels[labels.StateDir]
	if stateDir == "" {
		stateDir, err = containerutil.ContainerStateDirPath(globalOptions.Namespace, dataStore, container.ID())
		if err != nil {
			return emptyDigest, err
		}
	}

	lf, err := containerutil.Lock(stateDir)
	if err != nil {
		return emptyDigest, err
	}
	defer lf.Release()

	id := container.ID()
	info, err := container.Info(ctx)
	if err != nil {
		return emptyDigest, err
	}

	// NOTE: Moby uses provided rootfs to run container. It doesn't support
	// to commit container created by moby.
	baseImgWithoutPlatform, err := client.ImageService().Get(ctx, info.Image)
	if err != nil {
		return emptyDigest, fmt.Errorf("container %q lacks image (wasn't created by nerdctl?): %w", id, err)
	}
	platformLabel := info.Labels[labels.Platform]
	if platformLabel == "" {
		platformLabel = platforms.DefaultString()
		log.G(ctx).Warnf("Image lacks label %q, assuming the platform to be %q", labels.Platform, platformLabel)
	}
	ocispecPlatform, err := platforms.Parse(platformLabel)
	if err != nil {
		return emptyDigest, err
	}
	log.G(ctx).Debugf("ocispecPlatform=%q", platforms.Format(ocispecPlatform))
	platformMC := platforms.Only(ocispecPlatform)
	baseImg := containerd.NewImageWithPlatform(client, baseImgWithoutPlatform, platformMC)

	baseImgConfig, _, err := imgutil.ReadImageConfig(ctx, baseImg)
	if err != nil {
		return emptyDigest, err
	}

	// Ensure all the layers are here: https://github.com/containerd/nerdctl/issues/3425
	err = image.EnsureAllContent(ctx, client, baseImg.Name(), platformMC, globalOptions)
	if err != nil {
		log.G(ctx).Warn("Unable to fetch missing layers before committing. " +
			"If you try to save or push this image, it might fail. See https://github.com/containerd/nerdctl/issues/3439.")
	}

	if opts.Pause {
		task, err := container.Task(ctx, cio.Load)
		if err != nil {
			return emptyDigest, err
		}

		status, err := task.Status(ctx)
		if err != nil {
			return emptyDigest, err
		}

		switch status.Status {
		case containerd.Paused, containerd.Created, containerd.Stopped:
		default:
			if err := task.Pause(ctx); err != nil {
				return emptyDigest, fmt.Errorf("failed to pause container: %w", err)
			}

			defer func() {
				if err := task.Resume(ctx); err != nil {
					log.G(ctx).Warnf("failed to unpause container %v: %v", id, err)
				}
			}()
		}
	}

	var (
		differ = client.DiffService()
		snName = info.Snapshotter
		sn     = client.SnapshotService(snName)
	)

	// Don't gc me and clean the dirty data after 1 hour!
	ctx, done, err := client.WithLease(ctx, leases.WithRandomID(), leases.WithExpiration(1*time.Hour))
	if err != nil {
		return emptyDigest, fmt.Errorf("failed to create lease for commit: %w", err)
	}
	defer done(ctx)

	// Sync filesystem to make sure that all the data writes in container could be persisted to disk.
	Sync()

	if opts.ZstdChunked {
		opts.Compression = types.Zstd
	}
	diffLayerDesc, diffID, err := createDiff(ctx, id, sn, client.ContentStore(), differ, opts.Compression, opts)
	if err != nil {
		return emptyDigest, fmt.Errorf("failed to export layer: %w", err)
	}

	imageConfig, err := generateCommitImageConfig(ctx, container, baseImg, diffID, opts)
	if err != nil {
		return emptyDigest, fmt.Errorf("failed to generate commit image config: %w", err)
	}

	rootfsID := identity.ChainID(imageConfig.RootFS.DiffIDs).String()
	if err := applyDiffLayer(ctx, rootfsID, baseImgConfig, sn, differ, diffLayerDesc); err != nil {
		return emptyDigest, fmt.Errorf("failed to apply diff: %w", err)
	}

	commitManifestDesc, configDigest, err := writeContentsForImage(ctx, snName, baseImg, imageConfig, diffLayerDesc, opts)
	if err != nil {
		return emptyDigest, err
	}

	// image create
	img := images.Image{
		Name:      opts.Ref,
		Target:    commitManifestDesc,
		CreatedAt: time.Now(),
	}

	if _, err := client.ImageService().Update(ctx, img); err != nil {
		if !errdefs.IsNotFound(err) {
			return emptyDigest, err
		}

		if _, err := client.ImageService().Create(ctx, img); err != nil {
			return emptyDigest, fmt.Errorf("failed to create new image %s: %w", opts.Ref, err)
		}
	}

	// unpack the image to snapshotter
	cimg := containerd.NewImage(client, img)
	if err := cimg.Unpack(ctx, snName); err != nil {
		return emptyDigest, err
	}

	return configDigest, nil
}

// generateCommitImageConfig returns commit oci image config based on the container's image.
func generateCommitImageConfig(ctx context.Context, container containerd.Container, img containerd.Image, diffID digest.Digest, opts *Opts) (ocispec.Image, error) {
	spec, err := container.Spec(ctx)
	if err != nil {
		return ocispec.Image{}, err
	}

	baseConfig, _, err := imgutil.ReadImageConfig(ctx, img) // aware of img.platform
	if err != nil {
		return ocispec.Image{}, err
	}

	// TODO(fuweid): support updating the USER/ENV/... fields?
	if opts.Changes.CMD != nil {
		baseConfig.Config.Cmd = opts.Changes.CMD
	}
	if opts.Changes.Entrypoint != nil {
		baseConfig.Config.Entrypoint = opts.Changes.Entrypoint
	}
	if opts.Author == "" {
		opts.Author = baseConfig.Author
	}

	createdBy := ""
	if spec.Process != nil {
		createdBy = strings.Join(spec.Process.Args, " ")
	}

	createdTime := time.Now()
	arch := baseConfig.Architecture
	if arch == "" {
		arch = runtime.GOARCH
		log.G(ctx).Warnf("assuming arch=%q", arch)
	}
	os := baseConfig.OS
	if os == "" {
		os = runtime.GOOS
		log.G(ctx).Warnf("assuming os=%q", os)
	}
	log.G(ctx).Debugf("generateCommitImageConfig(): arch=%q, os=%q", arch, os)
	return ocispec.Image{
		Platform: ocispec.Platform{
			Architecture: arch,
			OS:           os,
		},

		Created: &createdTime,
		Author:  opts.Author,
		Config:  baseConfig.Config,
		RootFS: ocispec.RootFS{
			Type:    "layers",
			DiffIDs: append(baseConfig.RootFS.DiffIDs, diffID),
		},
		History: append(baseConfig.History, ocispec.History{
			Created:    &createdTime,
			CreatedBy:  createdBy,
			Author:     opts.Author,
			Comment:    opts.Message,
			EmptyLayer: (diffID == emptyGZLayer),
		}),
	}, nil
}

// writeContentsForImage will commit oci image config and manifest into containerd's content store.
func writeContentsForImage(ctx context.Context, snName string, baseImg containerd.Image, newConfig ocispec.Image, diffLayerDesc ocispec.Descriptor, opts *Opts) (ocispec.Descriptor, digest.Digest, error) {
	newConfigJSON, err := json.Marshal(newConfig)
	if err != nil {
		return ocispec.Descriptor{}, emptyDigest, err
	}

	// Select media types based on format choice
	var configMediaType, manifestMediaType string
	switch opts.Format {
	case types.ImageFormatOCI:
		configMediaType = ocispec.MediaTypeImageConfig
		manifestMediaType = ocispec.MediaTypeImageManifest
	case types.ImageFormatDocker:
		configMediaType = images.MediaTypeDockerSchema2Config
		manifestMediaType = images.MediaTypeDockerSchema2Manifest
	default:
		// Default to Docker Schema2 for compatibility
		configMediaType = images.MediaTypeDockerSchema2Config
		manifestMediaType = images.MediaTypeDockerSchema2Manifest
	}

	configDesc := ocispec.Descriptor{
		MediaType: configMediaType,
		Digest:    digest.FromBytes(newConfigJSON),
		Size:      int64(len(newConfigJSON)),
	}

	cs := baseImg.ContentStore()
	baseMfst, _, err := imgutil.ReadManifest(ctx, baseImg)
	if err != nil {
		return ocispec.Descriptor{}, emptyDigest, err
	}
	layers := append(baseMfst.Layers, diffLayerDesc)

	newMfst := struct {
		MediaType string `json:"mediaType,omitempty"`
		ocispec.Manifest
	}{
		MediaType: manifestMediaType,
		Manifest: ocispec.Manifest{
			Versioned: specs.Versioned{
				SchemaVersion: 2,
			},
			Config: configDesc,
			Layers: layers,
		},
	}

	newMfstJSON, err := json.MarshalIndent(newMfst, "", "    ")
	if err != nil {
		return ocispec.Descriptor{}, emptyDigest, err
	}

	newMfstDesc := ocispec.Descriptor{
		MediaType: manifestMediaType,
		Digest:    digest.FromBytes(newMfstJSON),
		Size:      int64(len(newMfstJSON)),
	}

	// new manifest should reference the layers and config content
	labels := map[string]string{
		"containerd.io/gc.ref.content.0": configDesc.Digest.String(),
	}
	for i, l := range layers {
		labels[fmt.Sprintf("containerd.io/gc.ref.content.%d", i+1)] = l.Digest.String()
	}

	err = content.WriteBlob(ctx, cs, newMfstDesc.Digest.String(), bytes.NewReader(newMfstJSON), newMfstDesc, content.WithLabels(labels))
	if err != nil {
		return ocispec.Descriptor{}, emptyDigest, err
	}

	// config should reference to snapshotter
	labelOpt := content.WithLabels(map[string]string{
		fmt.Sprintf("containerd.io/gc.ref.snapshot.%s", snName): identity.ChainID(newConfig.RootFS.DiffIDs).String(),
	})
	err = content.WriteBlob(ctx, cs, configDesc.Digest.String(), bytes.NewReader(newConfigJSON), configDesc, labelOpt)
	if err != nil {
		return ocispec.Descriptor{}, emptyDigest, err
	}

	return newMfstDesc, configDesc.Digest, nil
}

// createDiff creates a layer diff into containerd's content store.
func createDiff(ctx context.Context, name string, sn snapshots.Snapshotter, cs content.Store, comparer diff.Comparer, compression types.CompressionType, opts *Opts) (ocispec.Descriptor, digest.Digest, error) {
	diffOpts := make([]diff.Opt, 0)
	var mediaType string

	// Select media type based on format and compression
	switch opts.Format {
	case types.ImageFormatOCI:
		// Use OCI media types
		switch compression {
		case types.Zstd:
			diffOpts = append(diffOpts, diff.WithMediaType(ocispec.MediaTypeImageLayerZstd))
			mediaType = ocispec.MediaTypeImageLayerZstd
		default:
			diffOpts = append(diffOpts, diff.WithMediaType(ocispec.MediaTypeImageLayerGzip))
			mediaType = ocispec.MediaTypeImageLayerGzip
		}
	case types.ImageFormatDocker:
		// Use Docker Schema2 media types for compatibility
		switch compression {
		case types.Zstd:
			diffOpts = append(diffOpts, diff.WithMediaType(ocispec.MediaTypeImageLayerZstd))
			mediaType = images.MediaTypeDockerSchema2LayerZstd
		default:
			diffOpts = append(diffOpts, diff.WithMediaType(ocispec.MediaTypeImageLayerGzip))
			mediaType = images.MediaTypeDockerSchema2LayerGzip
		}
	default:
		// Default to Docker Schema2 media types for compatibility
		switch compression {
		case types.Zstd:
			diffOpts = append(diffOpts, diff.WithMediaType(ocispec.MediaTypeImageLayerZstd))
			mediaType = images.MediaTypeDockerSchema2LayerZstd
		default:
			diffOpts = append(diffOpts, diff.WithMediaType(ocispec.MediaTypeImageLayerGzip))
			mediaType = images.MediaTypeDockerSchema2LayerGzip
		}
	}

	newDesc, err := rootfs.CreateDiff(ctx, name, sn, comparer, diffOpts...)
	if err != nil {
		return ocispec.Descriptor{}, digest.Digest(""), err
	}

	info, err := cs.Info(ctx, newDesc.Digest)
	if err != nil {
		return ocispec.Descriptor{}, digest.Digest(""), err
	}

	diffIDStr, ok := info.Labels["containerd.io/uncompressed"]
	if !ok {
		return ocispec.Descriptor{}, digest.Digest(""), fmt.Errorf("invalid differ response with no diffID")
	}

	diffID, err := digest.Parse(diffIDStr)
	if err != nil {
		return ocispec.Descriptor{}, digest.Digest(""), err
	}

	// Convert to eStargz if requested
	if opts.Estargz {
		log.G(ctx).Infof("Converting diff layer to eStargz format")

		esgzOpts := []estargz.Option{
			estargz.WithCompressionLevel(opts.EstargzCompressionLevel),
		}
		if opts.EstargzChunkSize > 0 {
			esgzOpts = append(esgzOpts, estargz.WithChunkSize(opts.EstargzChunkSize))
		}
		if opts.EstargzMinChunkSize > 0 {
			esgzOpts = append(esgzOpts, estargz.WithMinChunkSize(opts.EstargzMinChunkSize))
		}

		convertFunc := estargzconvert.LayerConvertFunc(esgzOpts...)

		esgzDesc, err := convertFunc(ctx, cs, newDesc)
		if err != nil {
			return ocispec.Descriptor{}, digest.Digest(""), fmt.Errorf("failed to convert diff layer to eStargz: %w", err)
		} else if esgzDesc != nil {
			esgzDesc.MediaType = mediaType
			esgzInfo, err := cs.Info(ctx, esgzDesc.Digest)
			if err != nil {
				return ocispec.Descriptor{}, digest.Digest(""), err
			}

			esgzDiffIDStr, ok := esgzInfo.Labels["containerd.io/uncompressed"]
			if !ok {
				return ocispec.Descriptor{}, digest.Digest(""), fmt.Errorf("invalid differ response with no diffID")
			}

			esgzDiffID, err := digest.Parse(esgzDiffIDStr)
			if err != nil {
				return ocispec.Descriptor{}, digest.Digest(""), err
			}
			return ocispec.Descriptor{
				MediaType:   esgzDesc.MediaType,
				Digest:      esgzDesc.Digest,
				Size:        esgzDesc.Size,
				Annotations: esgzDesc.Annotations,
			}, esgzDiffID, nil
		}
	}

	// Convert to zstd:chunked if requested
	if opts.ZstdChunked {
		log.G(ctx).Infof("Converting diff layer to zstd:chunked format")

		esgzOpts := []estargz.Option{
			estargz.WithChunkSize(opts.ZstdChunkedChunkSize),
		}

		convertFunc := zstdchunkedconvert.LayerConvertFuncWithCompressionLevel(zstd.EncoderLevelFromZstd(opts.ZstdChunkedCompressionLevel), esgzOpts...)

		zstdchunkedDesc, err := convertFunc(ctx, cs, newDesc)
		if err != nil {
			return ocispec.Descriptor{}, digest.Digest(""), fmt.Errorf("failed to convert diff layer to zstd:chunked: %w", err)
		} else if zstdchunkedDesc != nil {
			zstdchunkedDesc.MediaType = mediaType
			zstdchunkedInfo, err := cs.Info(ctx, zstdchunkedDesc.Digest)
			if err != nil {
				return ocispec.Descriptor{}, digest.Digest(""), err
			}

			zstdchunkedDiffIDStr, ok := zstdchunkedInfo.Labels["containerd.io/uncompressed"]
			if !ok {
				return ocispec.Descriptor{}, digest.Digest(""), fmt.Errorf("invalid differ response with no diffID")
			}

			zstdchunkedDiffID, err := digest.Parse(zstdchunkedDiffIDStr)
			if err != nil {
				return ocispec.Descriptor{}, digest.Digest(""), err
			}
			return ocispec.Descriptor{
				MediaType:   zstdchunkedDesc.MediaType,
				Digest:      zstdchunkedDesc.Digest,
				Size:        zstdchunkedDesc.Size,
				Annotations: zstdchunkedDesc.Annotations,
			}, zstdchunkedDiffID, nil
		}
	}

	return ocispec.Descriptor{
		MediaType: mediaType,
		Digest:    newDesc.Digest,
		Size:      info.Size,
	}, diffID, nil
}

// applyDiffLayer will apply diff layer content created by createDiff into the snapshotter.
func applyDiffLayer(ctx context.Context, name string, baseImg ocispec.Image, sn snapshots.Snapshotter, differ diff.Applier, diffDesc ocispec.Descriptor) (retErr error) {
	var (
		key    = uniquePart() + "-" + name
		parent = identity.ChainID(baseImg.RootFS.DiffIDs).String()
	)

	mount, err := sn.Prepare(ctx, key, parent)
	if err != nil {
		return err
	}

	defer func() {
		if retErr != nil {
			// NOTE: the snapshotter should be hold by lease. Even
			// if the cleanup fails, the containerd gc can delete it.
			if err := sn.Remove(ctx, key); err != nil {
				log.G(ctx).Warnf("failed to cleanup aborted apply %s: %s", key, err)
			}
		}
	}()

	if _, err = differ.Apply(ctx, diffDesc, mount); err != nil {
		return err
	}

	if err = sn.Commit(ctx, name, key); err != nil {
		if errdefs.IsAlreadyExists(err) {
			return nil
		}
		return err
	}
	return nil
}

// copied from github.com/containerd/containerd/rootfs/apply.go
func uniquePart() string {
	t := time.Now()
	var b [3]byte
	// Ignore read failures, just decreases uniqueness
	rand.Read(b[:])
	return fmt.Sprintf("%d-%s", t.Nanosecond(), base64.URLEncoding.EncodeToString(b[:]))
}
