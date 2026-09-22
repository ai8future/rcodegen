import os from 'os'
import path from 'path'

export const CODE_DIR = path.resolve(
  process.env.RCODEGEN_CODE_DIR || path.join(os.homedir(), 'Desktop/_code')
)
