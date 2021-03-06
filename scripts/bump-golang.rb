#!/usr/bin/env ruby

require 'fileutils'
require 'yaml'

if ARGV.length != 1
  puts "Usage: bump-golang <version>"
  exit 1
end

def header(txt)
  puts "\e[32m### #{txt}\e[0m"
end

def text(txt)
  puts "\e[33m#{txt}\e[0m"
end

def from_version
  if @from_version
    return @from_version
  end

  package_name = Dir.glob("packages/golang*").first
  @from_version = package_name.match(/golang(\d*\.\d*\.\d*)/)[1]
end

def linux_filename(version)
  @linux_filename ||= "go#{version}.linux-amd64.tar.gz"
end

def windows_filename(version)
  @windows_filename ||= "go#{version}.windows-amd64.zip"
end

def check_process(process, action)
  if !process.success?
    raise "Command execution failed for #{action}"
  end
end

def add_blob(filename)
  `wget "https://redirector.gvt1.com/edgedl/go/#{filename}" -O "/tmp/#{filename}"`
  check_process($?, "downloading #{filename}")

  `bosh add-blob "/tmp/#{filename}" "golang/#{filename}" --sha2`
  check_process($?, "adding blob #{filename}")
end

def upload_blobs
  `bosh upload-blobs`
  check_process($?, "uploading blobs")
end

def move_packages(from_version, to_version)
  Dir.glob("packages/golang#{from_version}*").each do |src|
    dst = src.gsub(from_version, to_version)

    FileUtils.mv(src, dst)
    text "Moved #{src} to #{dst}"
  end
end

def bump_in_files(dir, from_version, to_version)
  Dir.glob("#{dir}/**/*").each do |target|
    if File.directory?(target)
      next
    end

    data = File.read(target)

    if data.include?(from_version)
      data.gsub!(from_version, to_version)

      File.open(target, "w") { |f| f.write(data) }
      text "Bumped in #{target}"
    end
  end
end

def sync_package_specs
  `./scripts/sync-package-specs`
  check_process($?, "deploying bosh lite")
end

def remove_blobs(from_version)
  blobs = YAML.parse(File.read("config/blobs.yml")).to_ruby
  blobs.delete_if { |k, v| k.include?(from_version) }
  File.open("config/blobs.yml", "w") { |f| f.write(blobs.to_yaml) }
end

to_version = ARGV[0]

header "Bumping golang from #{from_version} to #{to_version}..."
header "Adding blobs..."

add_blob(linux_filename(to_version))
add_blob(windows_filename(to_version))

header "Uploading blobs..."
upload_blobs

header "Renaming packages..."
move_packages(from_version, to_version)

header "Updating packages..."
bump_in_files("packages", from_version, to_version)

header "Updating jobs..."
bump_in_files("jobs", from_version, to_version)

header "Removing old blobs..."
remove_blobs(from_version)

header "Syncing package specs..."
sync_package_specs

header "Done."
