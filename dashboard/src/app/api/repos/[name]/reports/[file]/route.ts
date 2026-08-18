import { NextResponse } from 'next/server'
import fs from 'fs'
import path from 'path'
import { CODE_DIR } from '@/lib/code-dir'

function resolveWithin(baseDir: string, ...segments: string[]): string | null {
  const resolvedBase = path.resolve(baseDir)
  const resolvedTarget = path.resolve(resolvedBase, ...segments)
  // Ensure target is within base (prefix check with separator)
  const prefix = resolvedBase.endsWith(path.sep) ? resolvedBase : resolvedBase + path.sep
  if (!resolvedTarget.startsWith(prefix) && resolvedTarget !== resolvedBase) {
    return null
  }
  return resolvedTarget
}

export async function GET(
  request: Request,
  { params }: { params: Promise<{ name: string; file: string }> }
) {
  const { name, file } = await params

  // Validate path components
  const repoPath = resolveWithin(CODE_DIR, name)
  if (!repoPath) {
    return NextResponse.json({ error: 'Invalid repo path' }, { status: 400 })
  }

  const filePath = resolveWithin(repoPath, '_rcodegen', file)
  if (!filePath) {
    return NextResponse.json({ error: 'Invalid file path' }, { status: 400 })
  }

  // Require .md extension
  if (!filePath.endsWith('.md')) {
    return NextResponse.json({ error: 'Invalid file type' }, { status: 400 })
  }

  if (!fs.existsSync(filePath)) {
    return NextResponse.json({ error: 'Report not found' }, { status: 404 })
  }

  try {
    const content = fs.readFileSync(filePath, 'utf-8')
    return NextResponse.json({ content })
  } catch (error) {
    console.error('Error reading report:', error)
    return NextResponse.json({ error: 'Failed to read report' }, { status: 500 })
  }
}
