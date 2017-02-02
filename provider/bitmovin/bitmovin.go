package bitmovin

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/NYTimes/video-transcoding-api/config"
	"github.com/NYTimes/video-transcoding-api/db"
	"github.com/NYTimes/video-transcoding-api/provider"
	"github.com/bitmovin/bitmovin-go/bitmovin"
	"github.com/bitmovin/bitmovin-go/bitmovintypes"
	"github.com/bitmovin/bitmovin-go/models"
	"github.com/bitmovin/bitmovin-go/services"
)

// Name is the name used for registering the bitmovin provider in the
// registry of providers.
const Name = "bitmovin"

// Just to double check the interface is properly implemented
var _ provider.TranscodingProvider = (*bitmovinProvider)(nil)

func init() {
	provider.Register(Name, bitmovinConductorFactory)
}

var h264Levels = []bitmovintypes.H264Level{
	bitmovintypes.H264Level1,
	bitmovintypes.H264Level1b,
	bitmovintypes.H264Level1_1,
	bitmovintypes.H264Level1_2,
	bitmovintypes.H264Level1_3,
	bitmovintypes.H264Level2,
	bitmovintypes.H264Level2_1,
	bitmovintypes.H264Level2_2,
	bitmovintypes.H264Level3,
	bitmovintypes.H264Level3_1,
	bitmovintypes.H264Level3_2,
	bitmovintypes.H264Level4,
	bitmovintypes.H264Level4_1,
	bitmovintypes.H264Level4_2,
	bitmovintypes.H264Level5,
	bitmovintypes.H264Level5_1,
	bitmovintypes.H264Level5_2}

var errBitmovinInvalidConfig = provider.InvalidConfigError("missing Bitmovin api key. Please define the environment variable BITMOVIN_API_KEY set this value in the configuration file")

var s3Pattern = regexp.MustCompile(`^s3://`)

var httpPattern = regexp.MustCompile(`^http://`)
var httpsPattern = regexp.MustCompile(`^https://`)

type bitmovinProvider struct {
	client *bitmovin.Bitmovin
	config *config.Bitmovin
}

type bitmovinPreset struct {
	Video models.H264CodecConfiguration
	Audio models.AACCodecConfiguration
}

func (p *bitmovinProvider) CreatePreset(preset db.Preset) (string, error) {
	//Find a corresponding audio configuration that lines up, otherwise create it
	if strings.ToLower(preset.Audio.Codec) != "aac" {
		return "", fmt.Errorf("Unsupported Audio codec: %v", preset.Audio.Codec)
	}
	// Bitmovin supports H.264 and H.265, H.265 support can be added in the future
	if strings.ToLower(preset.Video.Codec) != "h264" {
		return "", fmt.Errorf("Unsupported Video codec: %v", preset.Video.Codec)
	}

	aac := services.NewAACCodecConfigurationService(p.client)
	var audioConfigID string
	bitrate, err := strconv.Atoi(preset.Audio.Bitrate)
	temp := int64(bitrate)
	fmt.Println(temp)
	audioConfig := &models.AACCodecConfiguration{
		Name:         stringToPtr(preset.Name),
		Bitrate:      &temp,
		SamplingRate: floatToPtr(48000.0),
	}
	audioResp, err := aac.Create(audioConfig)
	if err != nil {
		return "", err
	}
	if audioResp.Status == "ERROR" {
		return "", errors.New("Error in creating audio portion of Preset")
	}

	audioConfigID = *audioResp.Data.Result.ID

	customData := make(map[string]interface{})
	customData["audio"] = audioConfigID
	customData["container"] = preset.Container
	h264Config, err := p.createVideoPreset(preset, customData)

	h264 := services.NewH264CodecConfigurationService(p.client)
	videoResp, err := h264.Create(h264Config)
	if err != nil {
		return "", err
	}
	if videoResp.Status == "ERROR" {
		return "", errors.New("error in creating video portion of Preset")
	}
	return *videoResp.Data.Result.ID, nil
}

func (p *bitmovinProvider) createVideoPreset(preset db.Preset, customData map[string]interface{}) (*models.H264CodecConfiguration, error) {
	h264 := &models.H264CodecConfiguration{
		CustomData: customData,
	}
	profile := strings.ToLower(preset.Video.Profile)
	h264.Name = stringToPtr(preset.Name)
	switch profile {
	case "high":
		h264.Profile = bitmovintypes.H264ProfileHigh
	case "main":
		h264.Profile = bitmovintypes.H264ProfileMain
	case "baseline":
		h264.Profile = bitmovintypes.H264ProfileBaseline
	case "":
		h264.Profile = bitmovintypes.H264ProfileMain
	default:
		return nil, fmt.Errorf("Unrecognized H264 Profile: %v", preset.Video.Profile)
	}
	foundLevel := false
	for _, l := range h264Levels {
		if l == bitmovintypes.H264Level(preset.Video.ProfileLevel) {
			h264.Level = l
			foundLevel = true
			break
		}
	}
	if !foundLevel {
		// Just set it to the highest Level
		h264.Level = bitmovintypes.H264Level5_2
		// return nil, fmt.Errorf("Unrecognized H264 Level: %v", preset.Video.ProfileLevel)
	}
	if preset.Video.Width != "" {
		width, err := strconv.Atoi(preset.Video.Width)
		if err != nil {
			return nil, err
		}
		h264.Width = intToPtr(int64(width))
	}
	if preset.Video.Height != "" {
		height, err := strconv.Atoi(preset.Video.Height)
		if err != nil {
			return nil, err
		}
		h264.Height = intToPtr(int64(height))
	}

	if preset.Video.Bitrate == "" {
		return nil, errors.New("Video Bitrate must be set")
	}
	bitrate, err := strconv.Atoi(preset.Video.Bitrate)
	if err != nil {
		return nil, err
	}
	h264.Bitrate = intToPtr(int64(bitrate))
	if preset.Video.GopSize != "" {
		gopSize, err := strconv.Atoi(preset.Video.GopSize)
		if err != nil {
			return nil, err
		}
		h264.MaxGOP = intToPtr(int64(gopSize))
	}

	return h264, nil
}

func (p *bitmovinProvider) DeletePreset(presetID string) error {
	// Delete both the audio and video preset
	h264 := services.NewH264CodecConfigurationService(p.client)
	cdResp, err := h264.RetrieveCustomData(presetID)
	if err != nil {
		return err
	}
	if cdResp.Status == "ERROR" {
		return errors.New("")
	}
	var audioPresetID string
	if cdResp.Data.Result.CustomData != nil {
		cd := cdResp.Data.Result.CustomData
		i, ok := cd["audio"]
		if !ok {
			return errors.New("No Audio configuration found for Video Preset")
		}
		audioPresetID, ok = i.(string)
		if !ok {
			return errors.New("Audio Configuration somehow not a string")
		}
	} else {
		return errors.New("No Audio configuration found for Video Preset")
	}

	aac := services.NewAACCodecConfigurationService(p.client)
	audioDeleteResp, err := aac.Delete(audioPresetID)
	if err != nil {
		return err
	}
	if audioDeleteResp.Status == "ERROR" {
		return errors.New("Error in deleting audio portion of Preset")
	}

	videoDeleteResp, err := h264.Delete(presetID)
	if err != nil {
		return err
	}
	if videoDeleteResp.Status == "ERROR" {
		return errors.New("Error in deleting video portion of Preset")
	}
	return nil
}

func (p *bitmovinProvider) GetPreset(presetID string) (interface{}, error) {
	// Return a custom struct with the H264 and AAC config?
	h264 := services.NewH264CodecConfigurationService(p.client)
	response, err := h264.Retrieve(presetID)
	if err != nil {
		return nil, err
	}
	if response.Status == "ERROR" {
		return nil, errors.New("Error in retrieving video portion of Preset")
	}
	h264Config := response.Data.Result
	cd, err := h264.RetrieveCustomData(presetID)
	if err != nil {
		return nil, err
	}
	if cd.Status == "ERROR" {
		return nil, errors.New("")
	}
	// var aacConfigID string
	if cd.Data.Result.CustomData != nil {
		h264Config.CustomData = cd.Data.Result.CustomData
		i, ok := h264Config.CustomData["audio"]
		if !ok {
			return nil, errors.New("No Audio configuration found for Video Preset")
		}
		s, ok := i.(string)
		if !ok {
			return nil, errors.New("Audio Configuration somehow not a string")
		}
		aac := services.NewAACCodecConfigurationService(p.client)
		audioResponse, err := aac.Retrieve(s)
		if err != nil {
			return nil, err
		}
		if audioResponse.Status == "ERROR" {
			return nil, errors.New("Error in retrieving audio portion of Preset")
		}
		aacConfig := audioResponse.Data.Result
		preset := bitmovinPreset{
			Video: h264Config,
			Audio: aacConfig,
		}
		return preset, nil
	}
	return nil, errors.New("No Audio configuration found for Video Preset")

	// return preset, errors.New("Not implemented")
}

func (p *bitmovinProvider) Transcode(job *db.Job) (*provider.JobStatus, error) {

	// Parse the input, it will have a s3:// structure.  Grab the bucket and information, this will
	// be used on the output as well!

	// Scan through the outputs and see if hls is needed.  if so then a manifest generation will need to happen.
	// This will be a custom data on the encoding entry.

	// Scan through again and everything should be straight forward.  I will need the MP4 Muxing on the go client

	//Parse the input and set it up
	//It will be an s3 url so need to parse out the region and the bucket name

	bucketName, path, fileName, cloudRegion, err := parseS3URL(job.SourceMedia)
	if err != nil {
		return nil, err
	}

	aclEntry := models.ACLItem{
		Permission: bitmovintypes.ACLPermissionPublicRead,
	}
	acl := []models.ACLItem{aclEntry}

	s3IS := services.NewS3InputService(p.client)
	s3Input := &models.S3Input{
		BucketName:  stringToPtr(bucketName),
		AccessKey:   stringToPtr(p.config.AccessKeyID),
		SecretKey:   stringToPtr(p.config.SecretAccessKey),
		CloudRegion: cloudRegion,
	}

	s3ISResponse, err := s3IS.Create(s3Input)
	if err != nil {
		return nil, err
	} else if s3ISResponse.Status == "ERROR" {
		return nil, errors.New("Error in setting up S3 input")
	}

	s3OS := services.NewS3OutputService(p.client)
	s3Output := &models.S3Output{
		BucketName:  stringToPtr(bucketName),
		AccessKey:   stringToPtr(p.config.AccessKeyID),
		SecretKey:   stringToPtr(p.config.SecretAccessKey),
		CloudRegion: cloudRegion,
	}

	s3OSResponse, err := s3OS.Create(s3Output)
	if err != nil {
		fmt.Println(err)
		return nil, err
	} else if s3ISResponse.Status == "ERROR" {
		return nil, errors.New("Error in setting up S3 input")
	}

	inputPath := ""
	if path == "" {
		inputPath = fileName
	} else {
		inputPath = path + fileName
	}

	videoInputStream := models.InputStream{
		InputID:       s3ISResponse.Data.Result.ID,
		InputPath:     stringToPtr(inputPath),
		SelectionMode: bitmovintypes.SelectionModeAuto,
	}

	audioInputStream := models.InputStream{
		InputID:       s3ISResponse.Data.Result.ID,
		InputPath:     stringToPtr(inputPath),
		SelectionMode: bitmovintypes.SelectionModeAuto,
	}

	viss := []models.InputStream{videoInputStream}
	aiss := []models.InputStream{audioInputStream}

	h264S := services.NewH264CodecConfigurationService(p.client)

	var masterManifestPath string
	var masterManifestFile string
	outputtingHLS := false
	manifestID := ""

	//create the master manifest if needed so we can add it to the customData of the encoding response
	for _, output := range job.Outputs {
		videoPresetID := output.Preset.ProviderMapping[Name]
		customDataResp, err := h264S.RetrieveCustomData(videoPresetID)

		if err != nil {
			return nil, err
		}
		if customDataResp.Status == "ERROR" {
			return nil, errors.New("")
		}
		containerInterface, ok := customDataResp.Data.Result.CustomData["container"]
		if !ok {
			return nil, errors.New("")
		}
		container, ok := containerInterface.(string)
		if !ok {
			return nil, errors.New("")
		}
		if container == "m3u8" {
			outputtingHLS = true
			break
		}
	}

	hlsService := services.NewHLSManifestService(p.client)

	if outputtingHLS {
		masterManifestPath = filepath.Dir(job.StreamingParams.PlaylistFileName)
		masterManifestFile = filepath.Base(job.StreamingParams.PlaylistFileName)
		manifestOutput := models.Output{
			OutputID:   s3OSResponse.Data.Result.ID,
			OutputPath: stringToPtr(filepath.Join(path, masterManifestPath)),
			ACL:        acl,
		}
		hlsMasterManifest := &models.HLSManifest{
			ManifestName: stringToPtr(masterManifestFile),
			Outputs:      []models.Output{manifestOutput},
		}
		hlsMasterManifestResp, err := hlsService.Create(hlsMasterManifest)
		if err != nil {
			fmt.Println(err)
			return nil, err
		} else if hlsMasterManifestResp.Status == "ERROR" {
			return nil, errors.New("Error in HLS Master Manifest creation")
		}
		manifestID = *hlsMasterManifestResp.Data.Result.ID
	}

	encodingS := services.NewEncodingService(p.client)
	customData := make(map[string]interface{})
	if outputtingHLS {
		customData["manifest"] = manifestID
	}
	encoding := &models.Encoding{
		Name:        stringToPtr("encoding"),
		CustomData:  customData,
		CloudRegion: bitmovintypes.CloudRegionAWSUSEast1,
	}

	encodingResp, err := encodingS.Create(encoding)
	if err != nil {
		return nil, err
	} else if encodingResp.Status == "ERROR" {
		return nil, errors.New("Error in Encoding Creation")
	}

	// Order of operations
	// If HLS is needed, add MediaInfo for the Audio and StreamInfo for the Video.
	// Add the TS Muxings

	// If it is MP4, then simply mux the streams together into one MP4 file
	for _, output := range job.Outputs {
		videoPresetID := output.Preset.ProviderMapping[Name]
		videoResponse, err := h264S.Retrieve(videoPresetID)
		if err != nil {
			return nil, err
		}
		if videoResponse.Status == "ERROR" {
			return nil, errors.New("Error in retrieving video portion of preset")
		}
		customDataResp, err := h264S.RetrieveCustomData(videoPresetID)
		if err != nil {
			return nil, err
		}
		if customDataResp.Status == "ERROR" {
			return nil, errors.New("Error in retrieving video custom data where the audio ID and container type is stored")
		}
		audioPresetIDInterface, ok := customDataResp.Data.Result.CustomData["audio"]
		if !ok {
			return nil, errors.New("Audio ID not found in video custom data")
		}
		audioPresetID, ok := audioPresetIDInterface.(string)
		if !ok {
			return nil, errors.New("Audio ID somehow not a string")
		}

		var audioStreamID, videoStreamID string
		audioStream := &models.Stream{
			CodecConfigurationID: &audioPresetID,
			InputStreams:         aiss,
		}
		audioStreamResp, err := encodingS.AddStream(*encodingResp.Data.Result.ID, audioStream)
		if err != nil {
			return nil, err
		}
		if audioStreamResp.Status == "ERROR" {
			return nil, errors.New("Error in adding audio stream to Encoding")
		}
		audioStreamID = *audioStreamResp.Data.Result.ID

		videoStream := &models.Stream{
			CodecConfigurationID: &videoPresetID,
			InputStreams:         viss,
		}
		videoStreamResp, err := encodingS.AddStream(*encodingResp.Data.Result.ID, videoStream)
		if err != nil {
			return nil, err
		}
		if videoStreamResp.Status == "ERROR" {
			return nil, errors.New("Error in adding video stream to Encoding")
		}
		videoStreamID = *videoStreamResp.Data.Result.ID

		audioMuxingStream := models.StreamItem{
			StreamID: &audioStreamID,
		}
		videoMuxingStream := models.StreamItem{
			StreamID: &videoStreamID,
		}

		containerInterface, ok := customDataResp.Data.Result.CustomData["container"]
		if !ok {
			return nil, errors.New("Container type not found in video custom data")
		}
		container, ok := containerInterface.(string)
		if !ok {
			return nil, errors.New("Container type somehow not a string")
		}
		if container == "m3u8" {
			audioMuxingStream := models.StreamItem{
				StreamID: &audioStreamID,
			}
			audioMuxingOutput := models.Output{
				OutputID:   s3OSResponse.Data.Result.ID,
				OutputPath: stringToPtr(filepath.Join(path, masterManifestPath, audioPresetID)),
				ACL:        acl,
			}
			audioMuxing := &models.TSMuxing{
				SegmentLength: floatToPtr(float64(job.StreamingParams.SegmentDuration)),
				SegmentNaming: stringToPtr("seg_%number%.ts"),
				Streams:       []models.StreamItem{audioMuxingStream},
				Outputs:       []models.Output{audioMuxingOutput},
			}
			audioMuxingResp, err := encodingS.AddTSMuxing(*encodingResp.Data.Result.ID, audioMuxing)
			if err != nil {
				return nil, err
			}
			if audioMuxingResp.Status == "ERROR" {
				return nil, errors.New("Error in adding TS Muxing for audio")
			}
			// audioPresetIDToCreatedTSMuxingID[audioPresetID] = *audioMuxingResp.Data.Result.ID

			// create the MediaInfo
			audioMediaInfo := &models.MediaInfo{
				Type:            bitmovintypes.MediaTypeAudio,
				URI:             stringToPtr(audioPresetID + ".m3u8"),
				GroupID:         stringToPtr(audioPresetID),
				Language:        stringToPtr("en"),
				Name:            stringToPtr(audioPresetID),
				IsDefault:       boolToPtr(false),
				Autoselect:      boolToPtr(false),
				Forced:          boolToPtr(false),
				SegmentPath:     stringToPtr(audioPresetID),
				Characteristics: []string{"public.accessibility.describes-video"},
				EncodingID:      encodingResp.Data.Result.ID,
				StreamID:        audioStreamResp.Data.Result.ID,
				MuxingID:        audioMuxingResp.Data.Result.ID,
			}

			// Add to Master manifest, we will set the m3u8 and segments relative to the master

			audioMediaInfoResp, err := hlsService.AddMediaInfo(manifestID, audioMediaInfo)
			if err != nil {
				return nil, err
			}
			if audioMediaInfoResp.Status == "ERROR" {
				return nil, errors.New("Error in adding EXT-X-MEDIA")
			}

			// create the video ts muxing

			videoMuxingStream := models.StreamItem{
				StreamID: &videoStreamID,
			}

			// this needs to be handled correctly,
			// the video m3u8 MUST exist at the fileName specified.  Figure out the path using filepath
			// to the master manifest so that the relative URI's will all work.  set the segment output to be
			// in the same directory as the m3u8 file for simplicity.
			// relativePath, err := filepath.Rel(masterManifestPath, filepath.Dir(output.FileName))
			if err != nil {
				return nil, err
			}
			videoMuxingOutput := models.Output{
				OutputID: s3OSResponse.Data.Result.ID,
				//This path is only relative to the input file.
				OutputPath: stringToPtr(filepath.Join(path, masterManifestPath, videoPresetID)),
				ACL:        acl,
			}
			videoMuxing := &models.TSMuxing{
				SegmentLength: floatToPtr(float64(job.StreamingParams.SegmentDuration)),
				SegmentNaming: stringToPtr("seg_%number%.ts"),
				Streams:       []models.StreamItem{videoMuxingStream},
				Outputs:       []models.Output{videoMuxingOutput},
			}
			videoMuxingResp, err := encodingS.AddTSMuxing(*encodingResp.Data.Result.ID, videoMuxing)
			if err != nil {
				return nil, err
			}
			if videoMuxingResp.Status == "ERROR" {
				return nil, errors.New("Error in adding TS Muxing for video")
			}

			videoStreamInfo := &models.StreamInfo{
				Audio:       stringToPtr(audioPresetID),
				SegmentPath: stringToPtr(videoPresetID),
				URI:         stringToPtr(filepath.Base(output.FileName)),
				EncodingID:  encodingResp.Data.Result.ID,
				StreamID:    videoStreamResp.Data.Result.ID,
				MuxingID:    videoMuxingResp.Data.Result.ID,
			}

			videoStreamInfoResp, err := hlsService.AddStreamInfo(manifestID, videoStreamInfo)
			if err != nil {
				return nil, err
			}
			if videoStreamInfoResp.Status == "ERROR" {
				return nil, errors.New("Error in adding EXT-X-STREAM-INF")
			}

			// create the StreamInfo keeping in mind where all the paths are

		} else if container == "mp4" {
			videoMuxingOutput := models.Output{
				OutputID:   s3OSResponse.Data.Result.ID,
				ACL:        acl,
				OutputPath: stringToPtr(filepath.Join(path, filepath.Dir(output.FileName))),
			}
			videoMuxing := &models.MP4Muxing{
				Filename: stringToPtr(filepath.Base(output.FileName)),
				Outputs:  []models.Output{videoMuxingOutput},
				Streams:  []models.StreamItem{videoMuxingStream, audioMuxingStream},
			}
			videoMuxingResp, err := encodingS.AddMP4Muxing(*encodingResp.Data.Result.ID, videoMuxing)
			if err != nil {
				return nil, err
			}
			if videoMuxingResp.Status == "ERROR" {
				return nil, errors.New("Error in adding MP4 Muxing")
			}
		}
	}

	startResp, err := encodingS.Start(*encodingResp.Data.Result.ID)
	if err != nil {
		return nil, err
	}
	if startResp.Status == "ERROR" {
		return nil, errors.New("Error in starting encoding")
	}

	jobStatus := &provider.JobStatus{
		ProviderName:  Name,
		ProviderJobID: *encodingResp.Data.Result.ID,
		Status:        provider.StatusQueued,
	}

	return jobStatus, nil
}

func (p *bitmovinProvider) JobStatus(job *db.Job) (*provider.JobStatus, error) {
	// If the transcoding is finished, start manifest generation, wait (because it is fast),
	// and then return done, otherwise send the status of the transcoding

	// Do we need to analyze the input file here???
	// Not for now, add it later.

	fmt.Println("in JobStatus")

	encodingS := services.NewEncodingService(p.client)
	statusResp, err := encodingS.RetrieveStatus(job.ProviderJobID)
	// status := provider.JobStatus{}
	if err != nil {
		return nil, err
	}
	if statusResp.Status == "ERROR" {
		return &provider.JobStatus{
			ProviderName:  Name,
			ProviderJobID: job.ProviderJobID,
			Status:        provider.StatusFailed,
		}, nil
	}
	if *statusResp.Data.Result.Status == "FINISHED" {
		fmt.Println("should be generating manifest, dumping custom data response ")
		// see if manifest generation needs to happen
		cdResp, err := encodingS.RetrieveCustomData(job.ProviderJobID)
		// spew.Dump(cdResp)
		if err != nil {
			// need to check this
			return nil, err
		}
		if cdResp.Status == "ERROR" {
			// FIXME
			return nil, errors.New("No Custom Data on Encoding, there should at least be container information here")
		}
		cd := cdResp.Data.Result.CustomData
		i, ok := cd["manifest"]
		if !ok {
			// return done
			return &provider.JobStatus{
				ProviderName:  Name,
				ProviderJobID: job.ProviderJobID,
				Status:        provider.StatusFinished,
			}, nil
		}
		manifestID, ok := i.(string)
		if !ok {
			return nil, errors.New("Audio Configuration somehow not a string")
		}
		manifestS := services.NewHLSManifestService(p.client)
		// TODO:
		// Query manifest service, and do whatever needs to be done
		manifestStatusResp, err := manifestS.RetrieveStatus(manifestID)
		if err != nil {
			return nil, err
		}
		if *manifestStatusResp.Data.Result.Status == "ERROR" {
			return &provider.JobStatus{
				ProviderName:  Name,
				ProviderJobID: job.ProviderJobID,
				Status:        provider.StatusFailed,
			}, nil
		}

		if *manifestStatusResp.Data.Result.Status == "CREATED" {
			// start the manifest generation
			fmt.Println("starting manifest gen")
			fmt.Println(manifestID)
			startResp, err := manifestS.Start(manifestID)
			if err != nil {
				return nil, err
			} else if startResp.Status == "ERROR" {
				fmt.Println("some reason here")
				return &provider.JobStatus{
					ProviderName:  Name,
					ProviderJobID: job.ProviderJobID,
					Status:        provider.StatusFailed,
				}, nil
			}
			fmt.Println("returning started")
			return &provider.JobStatus{
				ProviderName:  Name,
				ProviderJobID: job.ProviderJobID,
				Status:        provider.StatusStarted,
			}, nil
		}

		if *manifestStatusResp.Data.Result.Status == "QUEUED" || *manifestStatusResp.Data.Result.Status == "RUNNING" {
			return &provider.JobStatus{
				ProviderName:  Name,
				ProviderJobID: job.ProviderJobID,
				Status:        provider.StatusStarted,
			}, nil
		}

		if *manifestStatusResp.Data.Result.Status == "FINISHED" {
			return &provider.JobStatus{
				ProviderName:  Name,
				ProviderJobID: job.ProviderJobID,
				Status:        provider.StatusFinished,
			}, nil
		}
	} else if *statusResp.Data.Result.Status == "CREATED" || *statusResp.Data.Result.Status == "QUEUED" {
		return &provider.JobStatus{
			ProviderName:  Name,
			ProviderJobID: job.ProviderJobID,
			Status:        provider.StatusQueued,
		}, nil
	} else if *statusResp.Data.Result.Status == "RUNNING" {
		return &provider.JobStatus{
			ProviderName:  Name,
			ProviderJobID: job.ProviderJobID,
			Status:        provider.StatusStarted,
		}, nil
	}

	return &provider.JobStatus{
		ProviderName:  Name,
		ProviderJobID: job.ProviderJobID,
		Status:        provider.StatusFailed,
	}, nil
}

func (p *bitmovinProvider) CancelJob(jobID string) error {
	// stop the job
	encodingS := services.NewEncodingService(p.client)
	resp, err := encodingS.Stop(jobID)
	if err != nil {
		return err
	}
	if resp.Status == "ERROR" {
		return errors.New("Error in canceling Job")
	}
	return nil
}

func (p *bitmovinProvider) Healthcheck() error {
	// Just going to call list encodings, and if it errors, then clearly it is unhealthy
	encodingS := services.NewEncodingService(p.client)
	resp, err := encodingS.List(int64(0), int64(1))
	if err != nil {
		return err
	}
	if resp.Status == "ERROR" {
		return errors.New("Bitmovin service unavailable")
	}
	return nil
}

func (p *bitmovinProvider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		InputFormats:  []string{"prores", "h264"},
		OutputFormats: []string{"mp4", "hls"},
		Destinations:  []string{"s3"},
	}
}

func bitmovinFactory(cfg *config.Config) (provider.TranscodingProvider, error) {
	if cfg.Bitmovin.APIKey == "" {
		return nil, errBitmovinInvalidConfig
	}
	client := bitmovin.NewBitmovin(cfg.Bitmovin.APIKey, cfg.Bitmovin.Endpoint, int64(cfg.Bitmovin.Timeout))
	return &bitmovinProvider{client: client, config: cfg.Bitmovin}, nil
}

func parseS3URL(input string) (bucketName string, path string, fileName string, cloudRegion bitmovintypes.AWSCloudRegion, err error) {
	if s3Pattern.MatchString(input) {
		truncatedInput := strings.TrimPrefix(input, "s3://")
		splitTruncatedInput := strings.Split(truncatedInput, "/")
		bucketName = splitTruncatedInput[0]
		fileName = splitTruncatedInput[len(splitTruncatedInput)-1]
		truncatedInput = strings.TrimPrefix(truncatedInput, bucketName+"/")
		path = strings.TrimSuffix(truncatedInput, fileName)
		cloudRegion = bitmovintypes.AWSCloudRegionUSEast1
		return
	}
	return "", "", "", bitmovintypes.AWSCloudRegion(""), errors.New("Could not parse S3 URL")
}

func bitmovinConductorFactory(cfg *config.Config) (provider.TranscodingProvider, error) {
	if cfg.Bitmovin.APIKey == "" || cfg.Bitmovin.AccessKeyID == "" || cfg.Bitmovin.SecretAccessKey == "" {
		return nil, errors.New("")
	}
	client := bitmovin.NewBitmovin(cfg.Bitmovin.APIKey, cfg.Bitmovin.Endpoint, int64(cfg.Bitmovin.Timeout))

	return &bitmovinProvider{client: client, config: cfg.Bitmovin}, nil
}

func stringToPtr(s string) *string {
	return &s
}

func intToPtr(i int64) *int64 {
	return &i
}

func boolToPtr(b bool) *bool {
	return &b
}

func floatToPtr(f float64) *float64 {
	return &f
}
