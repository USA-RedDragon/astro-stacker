"""Runs GraXpert's AI background extraction model on .npy planes.

Usage: graxpert_bge.py MODEL IN.npy OUT.npy [TILES]

IN is a float32 array, height x width x channels (1 or 3), values in
[0, 1]. OUT is the background model, the same shape. The steps follow
GraXpert 3.x's extract_background for the AI interpolation with no
smoothing, with its OpenCV resizing and blur done in NumPy, and the MAD
kept above zero for an image that is mostly one value. With TILES,
the model runs on a TILES x TILES grid of overlapping tiles a quarter of
the image across, blended together, so it follows structure four times
finer.
"""

import sys

import numpy as np
import onnxruntime as ort


def resize(img, width, height):
    """cv2.resize with INTER_LINEAR: half-pixel centres, edges clamped."""
    def axis(n_out, n_in):
        f = (np.arange(n_out) + 0.5) * (n_in / n_out) - 0.5
        i = np.floor(f).astype(int)
        t = f - i
        t[i < 0] = 0
        i[i < 0] = 0
        t[i >= n_in - 1] = 0
        i[i >= n_in - 1] = n_in - 1
        j = np.minimum(i + 1, n_in - 1)
        return i, j, t.astype(np.float32)

    yi, yj, yt = axis(height, img.shape[0])
    xi, xj, xt = axis(width, img.shape[1])
    rows = img[yi] * (1 - yt)[:, None, None] + img[yj] * yt[:, None, None]
    return rows[:, xi] * (1 - xt)[None, :, None] + rows[:, xj] * xt[None, :, None]


def blur(img, size=11, sigma=3.0):
    """cv2.GaussianBlur with its default border, reflect-101."""
    r = size // 2
    k = np.exp(-((np.arange(size) - r) ** 2) / (2 * sigma * sigma))
    k = (k / k.sum()).astype(np.float32)
    p = np.pad(img, ((r, r), (0, 0), (0, 0)), mode="reflect")
    img = sum(k[i] * p[i:i + img.shape[0]] for i in range(size))
    p = np.pad(img, ((0, 0), (r, r), (0, 0)), mode="reflect")
    return sum(k[i] * p[:, i:i + img.shape[1]] for i in range(size))


def background(session, img):
    padding = 8
    small = resize(img, 256 - 2 * padding, 256 - 2 * padding)
    small = np.pad(small, ((padding, padding), (padding, padding), (0, 0)), mode="edge")
    channels = small.shape[-1]
    median = [np.median(small[:, :, c]) for c in range(channels)]
    mad = [max(np.median(np.abs(small[:, :, c] - median[c])), 1e-9) for c in range(channels)]
    small = np.clip((small - median) / mad * 0.04, -1.0, 1.0)
    if channels == 1:
        small = np.repeat(small, 3, axis=-1)
    bg = session.run(None, {session.get_inputs()[0].name: small[None].astype(np.float32)})[0][0]
    if channels == 1:
        bg = bg[:, :, :1]
    bg = bg / 0.04 * mad + median
    bg = bg[padding:-padding, padding:-padding, :]
    bg = resize(blur(bg), img.shape[1], img.shape[0])
    return np.ascontiguousarray(bg, dtype=np.float32)


def tiled(session, img, n):
    h, w, _ = img.shape
    th, tw = h // 4, w // 4
    acc = np.zeros(img.shape, np.float64)
    weight = np.zeros((h, w, 1), np.float64)
    wy = np.sin(np.linspace(0, np.pi, th))[:, None] ** 2 + 1e-3
    wx = np.sin(np.linspace(0, np.pi, tw))[None, :] ** 2 + 1e-3
    wk = (wy * wx)[:, :, None]
    for i in range(n):
        for j in range(n):
            y0 = round(i * (h - th) / (n - 1))
            x0 = round(j * (w - tw) / (n - 1))
            acc[y0:y0 + th, x0:x0 + tw] += background(session, img[y0:y0 + th, x0:x0 + tw]) * wk
            weight[y0:y0 + th, x0:x0 + tw] += wk
    return np.ascontiguousarray(acc / weight, dtype=np.float32)


def main():
    model, src, dst = sys.argv[1:4]
    img = np.load(src).astype(np.float32)
    session = ort.InferenceSession(model, providers=["CPUExecutionProvider"])
    if len(sys.argv) > 4:
        np.save(dst, tiled(session, img, int(sys.argv[4])))
    else:
        np.save(dst, background(session, img))


if __name__ == "__main__":
    main()
