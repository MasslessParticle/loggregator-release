package syslog_test

import (
	"doppler/sinks/syslog"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cloudfoundry/dropsonde/emitter"
	"github.com/cloudfoundry/dropsonde/factories"
	"github.com/cloudfoundry/loggregatorlib/loggertesthelper"
	"github.com/cloudfoundry/sonde-go/events"

	"github.com/gogo/protobuf/proto"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("SyslogSink", func() {
	var (
		syslogSink            *syslog.SyslogSink
		sysLogger             *SyslogWriterRecorder
		syslogSinkRunFinished chan bool
		errorChannel          chan *events.Envelope
		errorHandler          func(string, string, string)
		inputChan             chan *events.Envelope
	)

	BeforeEach(func() {
		syslogSinkRunFinished = make(chan bool)
		sysLogger = NewSyslogWriterRecorder()
		errorChannel = make(chan *events.Envelope, 10)
		inputChan = make(chan *events.Envelope)

		errorHandler = func(errorMsg string, appId string, drainUrl string) {
			logMessage := factories.NewLogMessage(events.LogMessage_ERR, errorMsg, appId, "LGR")
			envelope, _ := emitter.Wrap(logMessage, "dropsonde-origin")

			select {
			case errorChannel <- envelope:
			default:
			}
		}

		syslogSink = syslog.NewSyslogSink("appId", "syslog://using-fake", loggertesthelper.Logger(), sysLogger, errorHandler, "dropsonde-origin")
	})

	Context("when remote syslog server is down", func() {
		BeforeEach(func() {
			sysLogger.SetDown(true)
			go func() {
				syslogSink.Run(inputChan)
				close(syslogSinkRunFinished)
			}()
		})

		AfterEach(func() {
			syslogSink.Disconnect()
			Eventually(syslogSinkRunFinished).Should(BeClosed())
		})

		It("still accepts messages without blocking", func(done Done) {
			logMessage, _ := emitter.Wrap(factories.NewLogMessage(events.LogMessage_OUT, "test message", "appId", "App"), "origin")

			for i := 0; i < 100; i++ {
				inputChan <- logMessage
			}
			close(done)
		})

		Context("when remote syslog server comes up", func() {
			BeforeEach(func() {
				for i := 0; i < 5; i++ {
					message, _ := emitter.Wrap(factories.NewLogMessage(events.LogMessage_OUT, fmt.Sprintf("test message: %d\n", i), "appId", "App"), "origin")
					inputChan <- message
				}
				close(inputChan)

				sysLogger.SetDown(false)
			})

			It("sends all the messages in the buffer", func(done Done) {
				Eventually(syslogSinkRunFinished).Should(BeClosed())
				data := sysLogger.ReceivedMessages()
				Expect(data).To(HaveLen(5))
				close(done)
			})
		})
	})

	Context("when remote syslog server is up", func() {
		BeforeEach(func() {
			go func() {
				syslogSink.Run(inputChan)
				close(syslogSinkRunFinished)
			}()
		})

		AfterEach(func() {
			syslogSink.Disconnect()
			Eventually(syslogSinkRunFinished).Should(BeClosed())
		})

		It("sends input messages to the syslog writer", func(done Done) {
			logMessage := factories.NewLogMessage(events.LogMessage_OUT, "test message", "appId", "App")
			logMessage.SourceInstance = proto.String("123")
			envelope, _ := emitter.Wrap(logMessage, "origin")

			inputChan <- envelope
			data := <-sysLogger.receivedChannel

			expectedSyslogMessage := fmt.Sprintf(`<14>1 test message ts: \d+ src: App srcId: 123`)
			Expect(string(data)).To(MatchRegexp(expectedSyslogMessage))
			close(done)
		})

		It("does not send non-log messages to the syslog writer", func(done Done) {
			nonLogMessage := factories.NewValueMetric("value-name", 2.0, "value-unit")
			envelope, _ := emitter.Wrap(nonLogMessage, "origin")

			inputChan <- envelope

			Consistently(sysLogger.receivedChannel).ShouldNot(Receive())
			close(done)
		})

		It("stops sending messages when the disconnect comes in", func(done Done) {
			logMessage, _ := emitter.Wrap(factories.NewLogMessage(events.LogMessage_OUT, "test message", "appId", "App"), "origin")
			inputChan <- logMessage

			_, ok := <-sysLogger.receivedChannel
			Expect(ok).To(BeTrue())

			syslogSink.Disconnect()

			Eventually(syslogSinkRunFinished).Should(BeClosed())

			close(done)
		})

		It("uses the timestamp of the logmessage when sending", func(done Done) {
			message, _ := emitter.Wrap(factories.NewLogMessage(events.LogMessage_OUT, "test message", "appId", "App"), "origin")
			expectedTimeString := fmt.Sprintf("ts: %d", message.GetLogMessage().GetTimestamp())

			time.Sleep(100 * time.Millisecond) //wait a bit to allow timestamps to differ

			inputChan <- message
			data := <-sysLogger.receivedChannel

			Expect(string(data)).To(MatchRegexp(expectedTimeString))
			close(done)
		})

		Context("when remote syslog server goes down", func() {
			BeforeEach(func() {
				sysLogger.SetDown(true)
			})

			It("reports error messages when it's connected", func(done Done) {
				logMessage, _ := emitter.Wrap(factories.NewLogMessage(events.LogMessage_OUT, "test message", "appId", "App"), "origin")
				inputChan <- logMessage
				errorLog := <-errorChannel
				errorMsg := string(errorLog.GetLogMessage().GetMessage())
				Expect(errorMsg).To(MatchRegexp(`Syslog Sink syslog://using-fake: Error when dialing out. Backing off for (\d+(\.\d+)?(m|u|µ)s). Err: Error connecting.`))
				Expect(errorLog.GetLogMessage().GetSourceType()).To(Equal("LGR"))

				close(done)
			})

			It("stops sending messages when the disconnect comes in", func(done Done) {
				logMessage, _ := emitter.Wrap(factories.NewLogMessage(events.LogMessage_OUT, "test message", "appId", "App"), "origin")
				inputChan <- logMessage

				Eventually(errorChannel).ShouldNot(BeEmpty())
				syslogSink.Disconnect()
				Eventually(syslogSinkRunFinished).Should(BeClosed())
				numErrors := len(errorChannel)

				logMessage, _ = emitter.Wrap(factories.NewLogMessage(events.LogMessage_OUT, "test message 2", "appId", "App"), "origin")

				Expect(inputChan).ShouldNot(BeSent(logMessage))
				close(inputChan)

				Expect(errorChannel).To(HaveLen(numErrors))
				close(done)
			})

			Context("when the buffer overflows", func() {
				BeforeEach(func() {
					for i := 0; i < 105; i++ {
						msg := fmt.Sprintf("message no %v", i)
						logMessage, _ := emitter.Wrap(factories.NewLogMessage(events.LogMessage_OUT, msg, "appId", "App"), "origin")

						inputChan <- logMessage
					}
					close(inputChan)
				})

				Context("when the remote syslog server comes back up again", func() {
					BeforeEach(func() {
						sysLogger.SetDown(false)
						Eventually(syslogSinkRunFinished).Should(BeClosed())
					})

					It("resumes sending messages", func(done Done) {
						data := sysLogger.ReceivedMessages()
						Expect(data).To(HaveLen(6))
						for i := 0; i < 5; i++ {
							msg := fmt.Sprintf("<14>1 message no %v", i+100)
							Expect(data[i+1]).To(MatchRegexp(msg))
						}
						close(done)
					})

					It("sends a message about the buffer overflow", func(done Done) {
						data := sysLogger.ReceivedMessages()
						Expect(len(data)).To(BeNumerically(">", 1))
						Expect(data[0]).To(MatchRegexp("<11>1 Log message output too high. We've dropped 100 messages"))
						close(done)
					})
				})
			})

		})
	})

	Describe("Disconnect", func() {
		It("is idempotent", func() {
			syslogSink.Disconnect()
			Expect(syslogSink.Disconnect).NotTo(Panic())
		})
	})
})

type SyslogWriterRecorder struct {
	receivedChannel  chan string
	receivedMessages []string
	down             bool
	connected        bool
	sync.Mutex
}

func NewSyslogWriterRecorder() *SyslogWriterRecorder {
	return &SyslogWriterRecorder{
		receivedChannel: make(chan string, 20),
		connected:       false,
	}
}

func (r *SyslogWriterRecorder) Connect() error {
	r.Lock()
	defer r.Unlock()
	if r.down {
		r.connected = false
		return errors.New("Error connecting.")
	} else {
		r.connected = true
		return nil
	}
}

func (r *SyslogWriterRecorder) Write(p int, b []byte, source, sourceId string, timestamp int64) (int, error) {
	r.Lock()
	defer r.Unlock()

	if r.down {
		return 0, errors.New("Error writing to stdout.")
	}

	messageString := fmt.Sprintf("<%d>1 %s ts: %d src: %s srcId: %s", p, string(b), timestamp, source, sourceId)
	r.receivedMessages = append(r.receivedMessages, messageString)
	r.receivedChannel <- messageString
	return len(b), nil
}

func (r *SyslogWriterRecorder) SetDown(newState bool) {
	r.Lock()
	defer r.Unlock()

	r.down = newState
}

func (r *SyslogWriterRecorder) IsConnected() bool {
	r.Lock()
	defer r.Unlock()
	return r.connected
}

func (r *SyslogWriterRecorder) SetConnected(newValue bool) {
	r.Lock()
	defer r.Unlock()
	r.connected = newValue
}

func (r *SyslogWriterRecorder) Close() error {
	return nil
}

func (r *SyslogWriterRecorder) ReceivedMessages() []string {
	r.Lock()
	defer r.Unlock()

	return r.receivedMessages
}
