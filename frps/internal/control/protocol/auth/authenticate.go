package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	controldomainruntime "github.com/zightch/frp/frps/internal/control/domain/runtime"
	controlprotocolerrors "github.com/zightch/frp/frps/internal/control/protocol/errors"
	controlrepo "github.com/zightch/frp/frps/internal/control/repo"
	"github.com/zightch/frp/frps/pkg/protocol"
)

type Repository interface {
	LoadGroupRuntimeByClientID(ctx context.Context, clientID [16]byte) (controldomainruntime.GroupRuntime, error)
}

type FrameReader interface {
	ReadFrame(conn net.Conn) (protocol.Frame, error)
}

type FrameReaderFunc func(conn net.Conn) (protocol.Frame, error)

func (fn FrameReaderFunc) ReadFrame(conn net.Conn) (protocol.Frame, error) {
	return fn(conn)
}

type AuthenticateOptions struct {
	Conn             net.Conn
	ExpectedClientID [16]byte
	Reader           FrameReader
	Writer           controlprotocolerrors.FrameWriter
	Repository       Repository
	Challenges       *ChallengeService
	ReadTimeout      time.Duration
}

type Result struct {
	Begin           protocol.AuthBegin
	Finish          protocol.AuthFinish
	Group           controldomainruntime.GroupRuntime
	FinishRequestID uint32
}

func Authenticate(options AuthenticateOptions) (Result, error) {
	if options.Conn == nil {
		return Result{}, fmt.Errorf("control connection is nil")
	}
	if options.Reader == nil {
		return Result{}, fmt.Errorf("auth frame reader is nil")
	}
	if options.Writer == nil {
		return Result{}, fmt.Errorf("auth frame writer is nil")
	}
	if options.Repository == nil {
		return Result{}, fmt.Errorf("auth repository is nil")
	}
	if options.Challenges == nil {
		return Result{}, fmt.Errorf("auth challenge service is nil")
	}

	beginFrame, err := options.Reader.ReadFrame(options.Conn)
	if err != nil {
		return Result{}, controlprotocolerrors.ReplyProtocolError(options.Writer, beginFrame, err)
	}
	begin, err := DecodeAuthBeginFrame(beginFrame, options.ExpectedClientID)
	if err != nil {
		return Result{}, controlprotocolerrors.ReplyProtocolError(options.Writer, beginFrame, err)
	}

	group, err := loadEnabledGroup(options.Repository, options.Writer, begin.ClientID, beginFrame.RequestID, options.ReadTimeout)
	if err != nil {
		return Result{}, err
	}

	challenge, err := options.Challenges.Issue(group.ClientSecretHash)
	if err != nil {
		return Result{}, err
	}
	challengeBody, err := protocol.MarshalAuthChallenge(challenge)
	if err != nil {
		return Result{}, err
	}
	if err := options.Writer.WriteFrame(protocol.Frame{
		Type:      protocol.TypeAuthChallenge,
		RequestID: beginFrame.RequestID,
		Body:      challengeBody,
	}); err != nil {
		return Result{}, err
	}

	finishFrame, err := options.Reader.ReadFrame(options.Conn)
	if err != nil {
		return Result{}, controlprotocolerrors.ReplyProtocolError(options.Writer, finishFrame, err)
	}
	finish, err := DecodeAuthFinishFrame(finishFrame)
	if err != nil {
		return Result{}, controlprotocolerrors.ReplyProtocolError(options.Writer, finishFrame, err)
	}
	if err := options.Challenges.Consume(finish.ChallengeID, finish.Response); err != nil {
		return Result{}, controlprotocolerrors.ReplyProtocolError(options.Writer, finishFrame, err)
	}

	group, err = loadEnabledGroup(options.Repository, options.Writer, begin.ClientID, finishFrame.RequestID, options.ReadTimeout)
	if err != nil {
		return Result{}, err
	}

	return Result{
		Begin:           begin,
		Finish:          finish,
		Group:           group,
		FinishRequestID: finishFrame.RequestID,
	}, nil
}

func loadEnabledGroup(repository Repository, writer controlprotocolerrors.FrameWriter, clientID [16]byte, requestID uint32, timeout time.Duration) (controldomainruntime.GroupRuntime, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	group, err := repository.LoadGroupRuntimeByClientID(ctx, clientID)
	cancel()
	if err != nil {
		switch {
		case errors.Is(err, controlrepo.ErrGroupNotFound):
			return controldomainruntime.GroupRuntime{}, controlprotocolerrors.ReplyError(writer, requestID, 0, protocol.ErrorCodeAuthInvalidClient, "client_id not found")
		default:
			return controldomainruntime.GroupRuntime{}, err
		}
	}
	if !group.Enabled {
		return controldomainruntime.GroupRuntime{}, controlprotocolerrors.ReplyError(writer, requestID, 0, protocol.ErrorCodeAuthGroupDisabled, "proxy group is disabled")
	}
	return group, nil
}
