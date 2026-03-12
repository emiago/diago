#/bin/zsh 

rsync -avP --delete --exclude='.git' --exclude='LICENSE.txt' --exclude='README.md' $(pwd)/public/ ../diago-public
