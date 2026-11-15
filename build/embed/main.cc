// stratus-embed: runs one ONNX Runtime graph over tensors read from stdin.
//
// It decides nothing. The server tokenises and resizes, sends a tensor and reads
// a vector back; which model, which input and what the vector means are all its
// business. One model per process, so the two towers come and go separately.
//
// Frames, little-endian:
//   request:  u8 dtype (1 = float32, 7 = int64), u8 rank, rank x u32 dims, data
//   response: u32 status (0 = ok), u32 count, count x float32 -- or, when status
//             is not 0, u32 length and that many bytes of message.
// EOF on stdin is the end of the process.

#include <onnxruntime_c_api.h>

#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fcntl.h>
#include <string>
#include <sys/mman.h>
#include <sys/stat.h>
#include <unistd.h>
#include <vector>

static const OrtApi *api;

static void die(const char *what, OrtStatus *st) {
  fprintf(stderr, "stratus-embed: %s: %s\n", what, st ? api->GetErrorMessage(st) : "");
  exit(1);
}

#define CHECK(what, call)                                                      \
  do {                                                                         \
    OrtStatus *st_ = (call);                                                   \
    if (st_) die(what, st_);                                                   \
  } while (0)

static bool readFull(void *p, size_t n) {
  auto *b = static_cast<uint8_t *>(p);
  while (n > 0) {
    ssize_t r = read(0, b, n);
    if (r <= 0) return false;
    b += r;
    n -= static_cast<size_t>(r);
  }
  return true;
}

static void writeFull(const void *p, size_t n) {
  auto *b = static_cast<const uint8_t *>(p);
  while (n > 0) {
    ssize_t w = write(1, b, n);
    if (w <= 0) exit(1);
    b += w;
    n -= static_cast<size_t>(w);
  }
}

static void fail(const std::string &msg) {
  uint32_t head[2] = {1, static_cast<uint32_t>(msg.size())};
  writeFull(head, sizeof head);
  writeFull(msg.data(), msg.size());
}

int main(int argc, char **argv) {
  if (argc != 2) {
    fprintf(stderr, "usage: stratus-embed MODEL.ort\n");
    return 2;
  }
  api = OrtGetApiBase()->GetApi(ORT_API_VERSION);

  // The model is mapped rather than read, and the session told to keep its
  // weights where they lie: file-backed pages the kernel can drop and read again
  // under pressure, instead of anonymous memory that can only be swapped.
  int fd = open(argv[1], O_RDONLY);
  struct stat sb;
  if (fd < 0 || fstat(fd, &sb) != 0) {
    perror(argv[1]);
    return 1;
  }
  void *model = mmap(nullptr, sb.st_size, PROT_READ, MAP_PRIVATE, fd, 0);
  if (model == MAP_FAILED) {
    perror("mmap");
    return 1;
  }

  OrtEnv *env;
  CHECK("env", api->CreateEnv(ORT_LOGGING_LEVEL_WARNING, "stratus-embed", &env));
  OrtSessionOptions *opts;
  CHECK("options", api->CreateSessionOptions(&opts));
  const char *threads = getenv("STRATUS_EMBED_THREADS");
  CHECK("threads", api->SetIntraOpNumThreads(opts, threads ? atoi(threads) : 1));
  CHECK("threads", api->SetInterOpNumThreads(opts, 1));
  CHECK("arena", api->DisableCpuMemArena(opts));
  CHECK("config", api->AddSessionConfigEntry(opts, "session.use_ort_model_bytes_directly", "1"));
  CHECK("config", api->AddSessionConfigEntry(opts, "session.use_ort_model_bytes_for_initializers", "1"));
  // Prepacking copies every weight into a layout of its own, which is the
  // anonymous copy the mapping above exists to avoid.
  CHECK("config", api->AddSessionConfigEntry(opts, "session.disable_prepacking", "1"));
  OrtSession *session;
  CHECK("session", api->CreateSessionFromArray(env, model, sb.st_size, opts, &session));

  OrtAllocator *alloc;
  CHECK("allocator", api->GetAllocatorWithDefaultOptions(&alloc));
  char *inName, *outName;
  CHECK("input", api->SessionGetInputName(session, 0, alloc, &inName));
  CHECK("output", api->SessionGetOutputName(session, 0, alloc, &outName));
  OrtMemoryInfo *mem;
  CHECK("memory", api->CreateCpuMemoryInfo(OrtArenaAllocator, OrtMemTypeDefault, &mem));

  std::vector<uint8_t> data;
  for (;;) {
    uint8_t head[2];
    if (!readFull(head, 2)) return 0;
    uint8_t dtype = head[0], rank = head[1];
    if (rank == 0 || rank > 8) return 3;
    uint32_t dims32[8];
    if (!readFull(dims32, rank * 4)) return 3;
    int64_t dims[8];
    size_t count = 1;
    for (int i = 0; i < rank; i++) {
      dims[i] = dims32[i];
      count *= dims32[i];
    }
    size_t width;
    ONNXTensorElementDataType type;
    switch (dtype) {
    case 1: width = 4; type = ONNX_TENSOR_ELEMENT_DATA_TYPE_FLOAT; break;
    case 7: width = 8; type = ONNX_TENSOR_ELEMENT_DATA_TYPE_INT64; break;
    default: return 3; // a framing error leaves nothing to resynchronise on
    }
    if (count > (64u << 20) / width) return 3;
    data.resize(count * width);
    if (!readFull(data.data(), data.size())) return 3;

    OrtValue *in = nullptr, *out = nullptr;
    OrtStatus *st = api->CreateTensorWithDataAsOrtValue(mem, data.data(), data.size(), dims, rank, type, &in);
    if (!st) st = api->Run(session, nullptr, &inName, &in, 1, &outName, 1, &out);
    if (in) api->ReleaseValue(in);
    if (st) {
      fail(api->GetErrorMessage(st));
      api->ReleaseStatus(st);
      continue;
    }
    OrtTensorTypeAndShapeInfo *info;
    size_t n = 0;
    float *v = nullptr;
    st = api->GetTensorTypeAndShape(out, &info);
    if (!st) {
      st = api->GetTensorShapeElementCount(info, &n);
      api->ReleaseTensorTypeAndShapeInfo(info);
    }
    if (!st) st = api->GetTensorMutableData(out, reinterpret_cast<void **>(&v));
    if (st) {
      fail(api->GetErrorMessage(st));
      api->ReleaseStatus(st);
    } else {
      uint32_t ok[2] = {0, static_cast<uint32_t>(n)};
      writeFull(ok, sizeof ok);
      writeFull(v, n * 4);
    }
    api->ReleaseValue(out);
  }
}
