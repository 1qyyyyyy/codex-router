"""从生成原图导出多尺寸图标，并编译视窗程序资源。"""

import argparse
import pathlib
import subprocess

from PIL import Image


parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--go', default='go', help='编译器路径')
args = parser.parse_args()
root = pathlib.Path(__file__).resolve().parent.parent
source = root / 'assets' / '路由图标.png'
icon = root / 'assets' / '路由图标.ico'
with Image.open(source) as image:
    image.convert('RGBA').save(
        icon,
        format='ICO',
        sizes=[(size, size) for size in (16, 20, 24, 32, 40, 48, 64, 128, 256)],
    )
subprocess.run(
    [args.go, 'run', 'github.com/akavel/rsrc@v0.10.2', '-arch', 'amd64',
     '-ico', str(icon), '-o', str(root / 'cmd' / 'rsrc_windows_amd64.syso')],
    cwd=root,
    check=True,
)
print('图标和程序资源已生成。')
