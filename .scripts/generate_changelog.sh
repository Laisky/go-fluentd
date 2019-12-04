#!/bin/bash
# Author: tim.tang
rm -f CHANGELOG.md

git for-each-ref --sort='*authordate' --format='%(tag)' refs/tags | grep -v '^$' | tac | while read TAG ; do
    echo
    if [ $NEXT ];then
        echo '       ' >> CHANGELOG.md
        echo *$NEXT* >> CHANGELOG.md
        echo '---' >> CHANGELOG.md
    else
        echo '       ' >> CHANGELOG.md
        echo *CURRENT* >> CHANGELOG.md
        echo '---' >> CHANGELOG.md
    fi

    echo '    ' >> CHANGELOG.md
    GIT_PAGER=cat git log --no-merges --date=short --invert-grep --grep=^Merge --pretty=format:'- %ad (%an) %s -> [view commit](http://gitlab.google.com.cn:10080/PaaS/go-fluentd/commit/%H)' $TAG..$NEXT >> CHANGELOG.md
    echo '    ' >> CHANGELOG.md
    NEXT=$TAG
done
echo "DONE."
