#!/usr/bin/env bash

# Licensed to the LF AI & Data foundation under one
# or more contributor license agreements. See the NOTICE file
# distributed with this work for additional information
# regarding copyright ownership. The ASF licenses this file
# to you under the Apache License, Version 2.0 (the
# "License"); you may not use this file except in compliance
# with the License. You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

function install_linux_deps() {
  if [[ -x "$(command -v apt)" ]]; then
    # for Ubuntu 18.04 & 20.04
    sudo apt install -y wget curl ca-certificates gnupg2  \
      g++ gcc gfortran git make ccache libssl-dev zlib1g-dev unzip \
      clang-format-10 clang-tidy-10 lcov libtool m4 autoconf automake python3 python3-pip \
      pkg-config uuid-dev libaio-dev libgoogle-perftools-dev 

    sudo pip3 install conan
  elif [[ -x "$(command -v yum)" ]]; then
    # for CentOS devtoolset-7
    sudo yum install -y epel-release centos-release-scl-rh 
    sudo yum install -y wget curl which \
      git make automake python3-devel \
      devtoolset-7-gcc devtoolset-7-gcc-c++ devtoolset-7-gcc-gfortran \
      llvm-toolset-7.0-clang llvm-toolset-7.0-clang-tools-extra \
      libaio libuuid-devel unzip \
      ccache lcov libtool m4 autoconf automake

    sudo pip3 install conan
    echo "source scl_source enable devtoolset-7" | sudo tee -a /etc/profile.d/devtoolset-7.sh
    echo "source scl_source enable llvm-toolset-7.0" | sudo tee -a /etc/profile.d/llvm-toolset-7.sh
    echo "export CLANG_TOOLS_PATH=/opt/rh/llvm-toolset-7.0/root/usr/bin" | sudo tee -a /etc/profile.d/llvm-toolset-7.sh
    source "/etc/profile.d/llvm-toolset-7.sh"
  else
    echo "Error Install Dependencies ..."
    exit 1
  fi
  # install cmake
  wget -qO- "https://cmake.org/files/v3.24/cmake-3.24.0-linux-x86_64.tar.gz" | sudo tar --strip-components=1 -xz -C /usr/local
}

function install_mac_deps() {
  sudo xcode-select --install > /dev/null 2>&1
  brew install conan libomp ninja cmake llvm ccache grep
  export PATH="/usr/local/opt/grep/libexec/gnubin:$PATH"
  brew update && brew upgrade && brew cleanup

  if [[ $(arch) == 'arm64' ]]; then
    brew install openssl
    brew install librdkafka
    brew install pkg-config
    sudo mkdir /usr/local/include
    sudo mkdir /usr/local/opt
    sudo ln -s "$(brew --prefix llvm)" "/usr/local/opt/llvm"
    sudo ln -s "$(brew --prefix libomp)/include/omp.h" "/usr/local/include/omp.h"
    sudo ln -s "$(brew --prefix libomp)" "/usr/local/opt/libomp"
    sudo ln -s "$(brew --prefix boost)/include/boost" "/usr/local/include/boost"
    sudo ln -s "$(brew --prefix tbb)/include/tbb" "/usr/local/include/tbb"
    sudo ln -s "$(brew --prefix tbb)/include/oneapi" "/usr/local/include/oneapi"
  fi
}

if ! command -v go &> /dev/null
then
    echo "go could not be found, please install it"
    exit
fi

if ! command -v cmake &> /dev/null
then
    echo "cmake could not be found, please install it"
    exit
fi

unameOut="$(uname -s)"
case "${unameOut}" in
    Linux*)     install_linux_deps;;
    Darwin*)    install_mac_deps;;
    *)          echo "Unsupported OS:${unameOut}" ; exit 0;
esac

