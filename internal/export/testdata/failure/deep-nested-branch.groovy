node { parallel A: { parallel B: { parallel X: { stage('X1') { sleep 3; catchError(buildResult: 'FAILURE', stageResult: 'FAILURE') { error 'caught' } } },
                                          C: { stage('C1') { echo 'c' } } },
                                 D: { stage('D1') { echo 'd' } } },
              E: { stage('E1') { sleep 1; error 'real' } } }
